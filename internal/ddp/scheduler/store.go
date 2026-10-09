package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/schedule"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// Plan records every elapsed occurrence and queues its complete execution chain.
// The scheduler calls it while holding the planner's advisory lock.
func Plan(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, now time.Time) error {
	if pool == nil || cfg == nil || now.IsZero() {
		return errors.New("planner requires pool, config and current time")
	}
	now = now.UTC().Truncate(time.Minute)
	definitions := jobs.Definitions(cfg)
	refs := make([]string, 0, len(definitions))
	for ref, job := range definitions {
		if job.Schedule != "" {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	for _, ref := range refs {
		job := definitions[ref]
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			return planJob(ctx, tx, cfg, ref, job, now)
		})
		if err != nil {
			return fmt.Errorf("plan %s: %w", ref, dbError(err))
		}
	}
	return nil
}

func planJob(ctx context.Context, tx pgx.Tx, cfg *config.Config, ref string, job config.Job, now time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO ops.planner_state(job_ref,planned_through) VALUES($1,$2) ON CONFLICT DO NOTHING`, ref, now.Add(-time.Minute)); err != nil {
		return err
	}
	var after time.Time
	if err := tx.QueryRow(ctx, `SELECT planned_through FROM ops.planner_state WHERE job_ref=$1 FOR UPDATE`, ref).Scan(&after); err != nil {
		return err
	}
	if !after.Before(now) {
		return nil
	}
	var paused bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT paused FROM ops.job_overrides WHERE job_ref=$1),false)`, ref).Scan(&paused); err != nil {
		return err
	}
	catchup := job.Catchup
	if catchup == "" {
		catchup = cfg.Scheduler.Defaults.Catchup
	}
	var newestID string
	var newestAt time.Time
	// ponytail: one transaction per root; batch recovery if outage history makes
	// transaction duration operationally significant.
	err := schedule.Each(job.Schedule, cfg.Ddp.Timezone, after, now, func(point schedule.Occurrence) error {
		reason := "missed occurrence"
		if paused {
			reason = "job paused"
		}
		tickID := ulid.Make().String()
		tag, err := tx.Exec(ctx, `INSERT INTO ops.ticks(id,job_ref,scheduled_at,local_time,timezone,status,reason) VALUES($1,$2,$3,$4,$5,'skipped',$6) ON CONFLICT(job_ref,timezone,local_time) DO NOTHING`, tickID, ref, point.At, point.Local, point.Timezone, reason)
		if err != nil {
			return err
		}
		newestAt = point.At
		newestID = ""
		if tag.RowsAffected() == 1 {
			newestID = tickID
		}
		return nil
	})
	if err != nil {
		return err
	}
	if newestID != "" && !paused && (newestAt.Equal(now) || catchup == "latest_only") {
		rootID, err := enqueueChain(ctx, tx, cfg, ref, newestAt)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ops.ticks SET status='launched',execution_id=$2,reason=NULL WHERE id=$1`, newestID, rootID); err != nil {
			return err
		}
	}

	_, err = tx.Exec(ctx, `UPDATE ops.planner_state SET planned_through=$2 WHERE job_ref=$1`, ref, now)
	return err
}

func enqueueChain(ctx context.Context, tx pgx.Tx, cfg *config.Config, rootRef string, at time.Time) (string, error) {
	refs := []string{rootRef}
	var err error
	if strings.HasPrefix(rootRef, "job/") {
		refs, err = Chain(cfg, rootRef)
		if err != nil {
			return "", err
		}
	}
	return enqueueRefs(ctx, tx, cfg, refs, at)
}

func enqueueRefs(ctx context.Context, tx pgx.Tx, cfg *config.Config, refs []string, at time.Time) (string, error) {
	rootRef := refs[0]
	ids := map[string]string{}
	rootID := ""
	for _, ref := range refs {
		run, err := jobs.EnqueueTx(ctx, tx, cfg, ref, at)
		if err != nil {
			return "", err
		}
		ids[ref] = run.ID
		if rootID == "" {
			rootID = run.ID
		}
		dependencies := []string{}
		if ref != rootRef {
			for _, parent := range cfg.Jobs[strings.TrimPrefix(ref, "job/")].After {
				dependencies = append(dependencies, ids[parent])
			}
		}
		sort.Strings(dependencies)
		dependencies = slices.Compact(dependencies)
		status := "queued"
		if len(dependencies) > 0 {
			status = "waiting"
		}
		if _, err := tx.Exec(ctx, `UPDATE ops.executions SET dispatch='scheduler',chain_id=$2,depends_on=$3,status=$4 WHERE id=$1`, run.ID, rootID, dependencies, status); err != nil {
			return "", err
		}
	}
	return rootID, nil
}

// advanceChains never joins unrelated runs: dependencies are saved execution IDs.
func advanceChains(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Each pass moves at least one level; repeating also records downstream skips.
		for {
			skipped, err := tx.Exec(ctx, `UPDATE ops.executions e SET status='skipped',finished_at=now(),reason=CASE WHEN EXISTS(SELECT FROM ops.job_overrides o WHERE o.job_ref=e.job_ref AND o.paused) THEN 'job paused' ELSE 'upstream execution did not succeed' END
    WHERE e.dispatch='scheduler' AND e.status IN ('waiting','queued') AND (
     EXISTS(SELECT FROM ops.job_overrides o WHERE o.job_ref=e.job_ref AND o.paused) OR
     EXISTS(SELECT FROM ops.executions p WHERE p.id=ANY(e.depends_on) AND p.status IN ('failed','interrupted','skipped')))`)
			if err != nil {
				return err
			}
			if skipped.RowsAffected() == 0 {
				break
			}
		}
		_, err := tx.Exec(ctx, `UPDATE ops.executions e SET status='queued' WHERE e.dispatch='scheduler' AND e.status='waiting'
   AND cardinality(e.depends_on)>0 AND (SELECT count(*) FROM ops.executions p WHERE p.id=ANY(e.depends_on) AND p.status='succeeded')=cardinality(e.depends_on)`)
		return err
	})
}

func dbError(err error) error {
	if err == nil {
		return nil
	}
	// Driver failures can include connection strings or raw row values.
	var pgerr interface{ SQLState() string }
	if errors.As(err, &pgerr) {
		if pgerr.SQLState() == "42501" {
			return fmt.Errorf("%w: database role lacks permission", audit.ErrRefused)
		}
		return fmt.Errorf("postgres %s", pgerr.SQLState())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Preserve our own bounded validation errors; connection errors are masked.
	var connect *pgconn.ConnectError
	if errors.As(err, &connect) {
		return errors.New("database connection failed")
	}
	return err
}
