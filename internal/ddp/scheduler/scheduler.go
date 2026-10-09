// Package scheduler plans durable cron ticks and runs their dependency chains.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/schedule"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

const lockKey = platform.SchedulerLockKey

// Run holds a dedicated advisory-lock connection until all its workers stop.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root string, log io.Writer) (runErr error) {
	if pool == nil || cfg == nil || cfg.Scheduler.MaxWorkers < 1 {
		return errors.New("scheduler requires a pool, config and positive max_workers")
	}
	if int64(pool.Config().MaxConns) < int64(cfg.Scheduler.MaxWorkers)+1 {
		return errors.New("scheduler pool needs at least max_workers + 1 connections")
	}
	definitions := jobs.Definitions(cfg)
	for ref, job := range definitions {
		if job.Schedule != "" {
			if _, err := schedule.Parse(job.Schedule, cfg.Ddp.Timezone); err != nil {
				return fmt.Errorf("%s: %w", ref, err)
			}
			if strings.HasPrefix(ref, "job/") {
				if _, err := Chain(cfg, ref); err != nil {
					return err
				}
			}
		}
	}
	owner, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return dbError(err)
	}
	defer owner.Close(context.Background())
	var locked bool
	if err := owner.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err != nil {
		return dbError(err)
	}
	if !locked {
		return errors.New("another scheduler holds the planner lock")
	}
	// Release explicitly so callers observe shutdown only after the lock is gone.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var unlocked bool
		if err := owner.QueryRow(cleanup, `SELECT pg_advisory_unlock($1)`, lockKey).Scan(&unlocked); err != nil {
			runErr = errors.Join(runErr, dbError(err))
		} else if !unlocked {
			runErr = errors.Join(runErr, errors.New("scheduler planner lock was not released"))
		}
	}()
	if _, err := jobs.RecoverScheduler(ctx, pool); err != nil {
		return err
	}
	if log == nil {
		log = io.Discard
	}
	instance := ulid.Make().String()
	workCtx, cancelWorkers := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWorkers()
	type completion struct {
		id  string
		run jobs.Execution
		err error
	}
	completed := make(chan completion, cfg.Scheduler.MaxWorkers)
	active := map[string]string{}
	keys := map[string]bool{}
	release := func(result completion) {
		jobRef := active[result.id]
		delete(active, result.id)
		delete(keys, "job:"+jobRef)
		if key := definitions[jobRef].ConcurrencyKey; key != "" {
			delete(keys, "shared:"+key)
		}
		_, _ = fmt.Fprintf(log, "scheduler execution=%s state=%s\n", result.run.ID, result.run.Status)
	}
	heartbeat := func(state string) error {
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		// Querying on the owning connection detects lost ownership before more launch.
		if err := owner.Ping(checkCtx); err != nil {
			return err
		}
		_, err := owner.Exec(checkCtx, `INSERT INTO ops.heartbeats(service_ref,instance_id,seen_at,state) VALUES('service/scheduler',$1,clock_timestamp(),$2) ON CONFLICT(service_ref) DO UPDATE SET instance_id=EXCLUDED.instance_id,seen_at=EXCLUDED.seen_at,state=EXCLUDED.state`, instance, state)
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastPlan := time.Time{}
	var loopErr error
loop:
	for ctx.Err() == nil {
		if err := heartbeat("running"); err != nil {
			loopErr = err
			break
		}
		now := time.Now().UTC().Truncate(time.Minute)
		if !now.Equal(lastPlan) {
			if err := Plan(ctx, pool, cfg, now); err != nil {
				loopErr = shutdownError(ctx, err)
				break
			}
			lastPlan = now
		}
		if err := advanceChains(ctx, pool); err != nil {
			loopErr = shutdownError(ctx, err)
			break
		}
		if len(active) < cfg.Scheduler.MaxWorkers {
			// The owning scheduler is the sole dispatcher. RunExecution independently
			// checks the saved due time and atomically claims each execution.
			rows, err := pool.Query(ctx, `SELECT id,job_ref FROM (SELECT DISTINCT ON(job_ref) id,job_ref,available_at FROM ops.executions WHERE dispatch='scheduler' AND status='queued' AND available_at<=clock_timestamp() ORDER BY job_ref,available_at,id) due ORDER BY available_at,id`)
			if err != nil {
				loopErr = shutdownError(ctx, err)
				break
			}
			type due struct{ id, ref string }
			candidates := []due{}
			for rows.Next() {
				var d due
				if err := rows.Scan(&d.id, &d.ref); err != nil {
					loopErr = err
					break
				}
				candidates = append(candidates, d)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				loopErr = shutdownError(ctx, err)
			}
			if loopErr != nil {
				break
			}
			for _, d := range candidates {
				if len(active) >= cfg.Scheduler.MaxWorkers || ctx.Err() != nil {
					break
				}
				job, known := definitions[d.ref]
				if !known {
					loopErr = fmt.Errorf("queued execution %s references undeclared %s", d.id, d.ref)
					break loop
				}
				if _, running := active[d.id]; running || keys["job:"+d.ref] || (job.ConcurrencyKey != "" && keys["shared:"+job.ConcurrencyKey]) {
					continue
				}
				active[d.id] = d.ref
				keys["job:"+d.ref] = true
				if job.ConcurrencyKey != "" {
					keys["shared:"+job.ConcurrencyKey] = true
				}
				go func() {
					run, err := jobs.RunExecution(workCtx, pool, cfg, root, "execution/"+d.id)
					completed <- completion{d.id, run, err}
				}()
			}
		}
		select {
		case result := <-completed:
			release(result)
			if result.err != nil && result.run.Status != "queued" && result.run.Status != "failed" && result.run.Status != "interrupted" && result.run.Status != "skipped" && result.run.Status != "succeeded" {
				loopErr = result.err
				break loop
			}
		case <-ticker.C:
		case <-ctx.Done():
			break loop
		}
	}
	// Consume planner cancellation before cleanup so later persistence failures
	// remain errors rather than being hidden by errors.Join with cancellation.
	if ctx.Err() != nil && errors.Is(loopErr, ctx.Err()) {
		loopErr = nil
	}
	if loopErr != nil {
		cancelWorkers()
	}
	// Normal shutdown allows a short grace before terminating process groups.
	grace := time.NewTimer(3 * time.Second)
	defer grace.Stop()
	for len(active) > 0 {
		select {
		case result := <-completed:
			release(result)
			if result.run.Status == "interrupted" {
				finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := pool.Exec(finish, `UPDATE ops.executions SET reason='scheduler shutdown' WHERE id=$1 AND dispatch='scheduler' AND status='interrupted' AND cancel_requested_at IS NULL`, result.run.ID)
				cancel()
				if err != nil {
					loopErr = errors.Join(loopErr, err)
				}
			}
		case <-grace.C:
			cancelWorkers()
		}
	}
	if err := heartbeat("stopped"); err != nil && ctx.Err() == nil {
		loopErr = errors.Join(loopErr, err)
	}
	return dbError(loopErr)
}

// pgx can return a raw socket timeout when cancellation interrupts a protocol
// write. Normalize it only at planner operations that use this same context.
func shutdownError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ctx.Err()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ctx.Err()
	}
	return err
}

// State is shared with continuous platform health.
type State = platform.SchedulerState

func Status(ctx context.Context, pool *pgxpool.Pool) (State, error) {
	return platform.ReadScheduler(ctx, pool)
}
