package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func schedulerDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ddp_scheduler_%d", time.Now().UnixNano())
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	pc, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = name
	pc.MaxConns = 6
	pool, err := pgxpool.NewWithConfig(t.Context(), pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(context.Background())
	})
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = migrate.Up(t.Context(), conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func schedulerConfig() *config.Config {
	return &config.Config{Ddp: config.Identity{Timezone: "UTC"}, Scheduler: config.Scheduler{MaxWorkers: 2, Defaults: config.Defaults{Timeout: "5s", Retry: config.Retry{MaxAttempts: 2, InitialDelay: "10ms", MaxDelay: "20ms"}, Catchup: "latest_only"}}, Jobs: map[string]config.Job{}, Models: map[string]config.Model{}}
}

func addModelJob(t *testing.T, cfg *config.Config, root, name, statement string, after ...string) {
	t.Helper()
	file := name + ".sql"
	if err := os.WriteFile(filepath.Join(root, file), []byte(statement), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Models["mart."+name] = config.Model{File: file, Materialization: "materialized_view", Contract: config.Contract{Columns: map[string]config.Column{"id": {Type: "integer"}}, UniqueKey: []string{"id"}}}
	cfg.Jobs[name] = config.Job{Action: "transform", Model: "model/mart." + name, After: after}
}

func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(12 * time.Second)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scheduler condition did not become true")
}

func startScheduler(t *testing.T, pool *pgxpool.Pool, cfg *config.Config, root string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, pool, cfg, root, nil) }()
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("scheduler: %v", err)
			}
		case <-time.After(12 * time.Second):
			t.Error("scheduler did not stop")
		}
	})
	t.Cleanup(stop)
	waitFor(t, func() bool {
		state, err := Status(t.Context(), pool)
		return err == nil && state.State == "running" && state.LockHeld
	})
	return stop
}

func TestTickPlanningAndDiamondChain(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id`)
	addModelJob(t, cfg, root, "left", `SELECT 1::integer AS id FROM pg_sleep(0.1)`, "job/root")
	addModelJob(t, cfg, root, "right", `SELECT 1::integer AS id FROM pg_sleep(0.1)`, "job/root")
	addModelJob(t, cfg, root, "join", `SELECT 1::integer AS id`, "job/left", "job/right", "job/right")
	job := cfg.Jobs["root"]
	job.Schedule = "* * * * *"
	cfg.Jobs["root"] = job
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	for range 2 {
		if err := Plan(t.Context(), pool, cfg, now); err != nil {
			t.Fatal(err)
		}
	}
	var ticks, runs int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ops.ticks WHERE job_ref='job/root'),(SELECT count(*) FROM ops.executions WHERE job_ref IN ('job/root','job/left','job/right','job/join'))`).Scan(&ticks, &runs); err != nil || ticks != 1 || runs != 4 {
		t.Fatalf("duplicate planning: %d %d %v", ticks, runs, err)
	}
	job.Schedule = ""
	cfg.Jobs["root"] = job
	stop := startScheduler(t, pool, cfg, root)
	if err := Run(t.Context(), pool, cfg, root, nil); err == nil || !strings.Contains(err.Error(), "planner lock") {
		t.Fatalf("second planner accepted: %v", err)
	}
	waitFor(t, func() bool {
		var n int
		return pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.executions WHERE job_ref IN ('job/root','job/left','job/right','job/join') AND status='succeeded'`).Scan(&n) == nil && n == 4
	})
	var ordered bool
	if err := pool.QueryRow(t.Context(), `SELECT bool_and(child.started_at>=parent.finished_at) FROM ops.executions child JOIN ops.executions parent ON parent.id=ANY(child.depends_on)`).Scan(&ordered); err != nil || !ordered {
		t.Fatalf("dependency order: %v %v", ordered, err)
	}
	stop()
	state, err := Status(t.Context(), pool)
	if err != nil || state.State != "stopped" || state.LockHeld {
		t.Fatalf("lock or heartbeat leaked: %+v %v", state, err)
	}
}

func TestRejectedSchedulerDoesNotLeakOwnerConnection(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	stop := startScheduler(t, pool, cfg, t.TempDir())
	defer stop()

	var before int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if err := Run(t.Context(), pool, cfg, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "planner lock") {
			t.Fatalf("contender result: %v", err)
		}
	}
	waitFor(t, func() bool {
		var after int
		return pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend'`).Scan(&after) == nil && after <= before+1
	})
}

func TestShutdownCancelsActiveWorkerAndPersistsOutcome(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "slow", `SELECT 1::integer AS id FROM pg_sleep(10)`)
	queued, err := QueueRun(t.Context(), pool, cfg, "job/slow", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, pool, cfg, root, nil) }()
	waitFor(t, func() bool {
		var status string
		return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, queued.ExecutionID).Scan(&status) == nil && status == "running"
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("scheduler did not shut down")
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, queued.ExecutionID).Scan(&status); err != nil || status != "interrupted" {
		t.Fatalf("execution status=%q err=%v", status, err)
	}
}

func TestShutdownErrorNormalizesCanceledNetworkTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := &net.DNSError{Err: "write timeout", IsTimeout: true}
	if got := shutdownError(ctx, err); !errors.Is(got, context.Canceled) {
		t.Fatalf("shutdown error = %v, want context canceled", got)
	}
	active := context.Background()
	if got := shutdownError(active, err); got != err {
		t.Fatalf("active error = %v, want original error", got)
	}
}

func TestCommsRelayPlansOnceAndDispatchesDurably(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	cfg.Comms.SMTP = &config.SMTP{Addr: "localhost:2525", From: "ddp@example.com", TLS: "none"}
	now := time.Now().UTC().Truncate(time.Minute)
	if err := Plan(t.Context(), pool, cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := Plan(t.Context(), pool, cfg, now); err != nil {
		t.Fatal(err)
	}
	var ticks, executions int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ops.ticks WHERE job_ref=$1),(SELECT count(*) FROM ops.executions WHERE job_ref=$1)`, jobs.CommsRelayRef).Scan(&ticks, &executions); err != nil {
		t.Fatal(err)
	}
	if ticks != 1 || executions != 1 {
		t.Fatalf("relay duplicate planning: ticks=%d executions=%d", ticks, executions)
	}
	var executionID string
	if err := pool.QueryRow(t.Context(), `SELECT id FROM ops.executions WHERE job_ref=$1`, jobs.CommsRelayRef).Scan(&executionID); err != nil {
		t.Fatal(err)
	}
	if jobs.Definitions(cfg)[jobs.CommsRelayRef].Schedule == "" {
		t.Fatal("relay is not scheduled with SMTP")
	}
	startScheduler(t, pool, cfg, t.TempDir())
	waitFor(t, func() bool {
		var status string
		return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, executionID).Scan(&status) == nil && status == "succeeded"
	})
	var attempts int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.attempts WHERE execution_id=$1`, executionID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("relay durable attempt: %d %v", attempts, err)
	}
}

func TestHealthTickPlansAndRecordsDurableAttempt(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	now := time.Now().UTC().Truncate(time.Minute)
	if err := Plan(t.Context(), pool, cfg, now); err != nil {
		t.Fatal(err)
	}
	var executionID string
	if err := pool.QueryRow(t.Context(), `SELECT id FROM ops.executions WHERE job_ref=$1`, jobs.HealthRef).Scan(&executionID); err != nil {
		t.Fatal(err)
	}
	startScheduler(t, pool, cfg, t.TempDir())
	waitFor(t, func() bool {
		var status string
		return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, executionID).Scan(&status) == nil && status == "succeeded"
	})
	var attempts int
	var result []byte
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.attempts WHERE execution_id=$1`, executionID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("health durable attempt: attempts=%d err=%v", attempts, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT result FROM ops.attempts WHERE execution_id=$1`, executionID).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "state_counts") || strings.Contains(string(result), "message") {
		t.Fatalf("health attempt result was not reduced to state counts: %s", result)
	}
}

func TestCommsRelayQueuedWithoutSMTPFailsDurably(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	cfg.Comms.SMTP = &config.SMTP{Addr: "localhost:2525", From: "ddp@example.com", TLS: "none"}
	run, err := jobs.Enqueue(t.Context(), pool, cfg, jobs.CommsRelayRef, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET dispatch='scheduler' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	cfg.Comms.SMTP = nil
	startScheduler(t, pool, cfg, t.TempDir())
	waitFor(t, func() bool {
		var status string
		return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, run.ID).Scan(&status) == nil && status == "failed"
	})
}

func TestCommsRelayPauseSkipsTicksWithoutReplay(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	cfg.Comms.SMTP = &config.SMTP{Addr: "localhost:2525", From: "ddp@example.com", TLS: "none"}
	if _, err := SetPaused(t.Context(), pool, cfg, jobs.CommsRelayRef, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	if err := Plan(t.Context(), pool, cfg, now); err != nil {
		t.Fatal(err)
	}
	if _, err := SetPaused(t.Context(), pool, cfg, jobs.CommsRelayRef, false); err != nil {
		t.Fatal(err)
	}
	if err := Plan(t.Context(), pool, cfg, now); err != nil {
		t.Fatal(err)
	}
	var skipped, executions int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ops.ticks WHERE job_ref=$1 AND status='skipped'),(SELECT count(*) FROM ops.executions WHERE job_ref=$1)`, jobs.CommsRelayRef).Scan(&skipped, &executions); err != nil {
		t.Fatal(err)
	}
	if skipped == 0 || executions != 0 {
		t.Fatalf("relay pause replay: skipped=%d executions=%d", skipped, executions)
	}
}

func TestMissedTicksCatchupAndFailureStopsChain(t *testing.T) {
	for _, catchup := range []string{"none", "latest_only"} {
		t.Run(catchup, func(t *testing.T) {
			pool := schedulerDB(t)
			cfg := schedulerConfig()
			root := t.TempDir()
			addModelJob(t, cfg, root, "bad", `SELECT 1/0 AS id`)
			addModelJob(t, cfg, root, "child", `SELECT 1::integer AS id`, "job/bad")
			job := cfg.Jobs["bad"]
			job.Schedule = "*/2 * * * *"
			job.Catchup = catchup
			cfg.Jobs["bad"] = job
			now := time.Date(2026, 9, 4, 10, 5, 0, 0, time.UTC)
			if _, err := pool.Exec(t.Context(), `INSERT INTO ops.planner_state VALUES('job/bad',$1)`, now.Add(-5*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := Plan(t.Context(), pool, cfg, now); err != nil {
				t.Fatal(err)
			}
			var ticks, launched int
			if err := pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE status='launched') FROM ops.ticks WHERE job_ref='job/bad'`).Scan(&ticks, &launched); err != nil || ticks != 2 {
				t.Fatalf("missed ticks: %d %d %v", ticks, launched, err)
			}
			want := 0
			if catchup == "latest_only" {
				want = 1
			}
			if launched != want {
				t.Fatalf("catchup launched %d want %d", launched, want)
			}
			if want == 0 {
				return
			}
			job.Schedule = ""
			cfg.Jobs["bad"] = job
			stop := startScheduler(t, pool, cfg, root)
			waitFor(t, func() bool {
				var status string
				return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE job_ref='job/child'`).Scan(&status) == nil && status == "skipped"
			})
			var attempts int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.attempts a JOIN ops.executions e ON e.id=a.execution_id WHERE e.job_ref IN ('job/bad','job/child')`).Scan(&attempts); err != nil || attempts != 2 {
				t.Fatalf("retry limit or skipped child: %d %v", attempts, err)
			}
			stop()
		})
	}
}

func TestWorkerBoundAndSharedKey(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	for _, name := range []string{"one", "two", "three"} {
		addModelJob(t, cfg, root, name, `SELECT 1::integer AS id FROM pg_sleep(0.4)`)
		job := cfg.Jobs[name]
		if name != "three" {
			job.ConcurrencyKey = "vendor"
		}
		cfg.Jobs[name] = job
		run, err := jobs.Enqueue(t.Context(), pool, cfg, "job/"+name, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET dispatch='scheduler' WHERE id=$1`, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	stop := startScheduler(t, pool, cfg, root)
	waitFor(t, func() bool {
		var running, finished int
		err := pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE status='running'),count(*) FILTER(WHERE status='succeeded') FROM ops.executions WHERE job_ref IN ('job/one','job/two','job/three')`).Scan(&running, &finished)
		if running > 2 {
			t.Error("worker bound exceeded")
		}
		return err == nil && finished == 3
	})
	var overlapped bool
	if err := pool.QueryRow(t.Context(), `SELECT a.started_at<b.finished_at AND b.started_at<a.finished_at FROM ops.executions a,ops.executions b WHERE a.job_ref='job/one' AND b.job_ref='job/two'`).Scan(&overlapped); err != nil || overlapped {
		t.Fatalf("shared key overlapped: %v %v", overlapped, err)
	}
	stop()
}

func TestPlannerDSTPauseAndAtomicRollback(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	cfg.Ddp.Timezone = "America/New_York"
	root := t.TempDir()
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id`)
	job := cfg.Jobs["root"]
	job.Schedule = "30 1 * * *"
	cfg.Jobs["root"] = job
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	for _, at := range []time.Time{first, first.Add(time.Hour)} {
		if err := Plan(t.Context(), pool, cfg, at); err != nil {
			t.Fatal(err)
		}
	}
	var ticks, runs int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ops.ticks WHERE job_ref='job/root'),(SELECT count(*) FROM ops.executions WHERE job_ref='job/root')`).Scan(&ticks, &runs); err != nil || ticks != 1 || runs != 1 {
		t.Fatalf("repeated wall time: ticks=%d runs=%d err=%v", ticks, runs, err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO ops.job_overrides(job_ref,paused) VALUES('job/root',true)`); err != nil {
		t.Fatal(err)
	}
	next := first.Add(25 * time.Hour)
	if err := Plan(t.Context(), pool, cfg, next); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.job_overrides SET paused=false`); err != nil {
		t.Fatal(err)
	}
	if err := Plan(t.Context(), pool, cfg, next); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := pool.QueryRow(t.Context(), `SELECT reason FROM ops.ticks WHERE scheduled_at=$1 AND job_ref='job/root'`, next).Scan(&reason); err != nil || reason != "job paused" {
		t.Fatalf("paused tick: %q %v", reason, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.executions WHERE job_ref='job/root'`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("pause replay: %d %v", runs, err)
	}

	// Failure while constructing a descendant must roll back the tick, the root
	// execution and the planner cursor together.
	addModelJob(t, cfg, root, "child", `SELECT 1::integer AS id`, "job/root")
	child := cfg.Jobs["child"]
	child.Retry = &config.Retry{MaxAttempts: 0}
	cfg.Jobs["child"] = child
	if err := Plan(t.Context(), pool, cfg, next.Add(24*time.Hour)); err == nil {
		t.Fatal("invalid chain was accepted")
	}
	var cursor time.Time
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ops.ticks WHERE job_ref='job/root'),(SELECT count(*) FROM ops.executions WHERE job_ref='job/root'),planned_through FROM ops.planner_state WHERE job_ref='job/root'`).Scan(&ticks, &runs, &cursor); err != nil || ticks != 2 || runs != 1 || !cursor.Equal(next) {
		t.Fatalf("partial plan: ticks=%d runs=%d cursor=%s err=%v", ticks, runs, cursor, err)
	}
}

func TestPlannerLongOutage(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id`)
	job := cfg.Jobs["root"]
	job.Schedule = "* * * * *"
	cfg.Jobs["root"] = job
	now := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(t.Context(), `INSERT INTO ops.planner_state VALUES('job/root',$1)`, now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := Plan(t.Context(), pool, cfg, now); err != nil {
		t.Fatal(err)
	}
	var ticks, launched int
	if err := pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE status='launched') FROM ops.ticks WHERE job_ref='job/root'`).Scan(&ticks, &launched); err != nil || ticks != 8*24*60 || launched != 1 {
		t.Fatalf("long outage: %d ticks, %d launched, %v", ticks, launched, err)
	}
}

func TestManualExecutionSharesSchedulerJobLock(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id FROM pg_sleep(0.4)`)
	manual, err := jobs.Enqueue(t.Context(), pool, cfg, "job/root", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := jobs.RunExecution(t.Context(), pool, cfg, root, "execution/"+manual.ID); done <- err }()
	waitFor(t, func() bool {
		var running bool
		return pool.QueryRow(t.Context(), `SELECT status='running' FROM ops.executions WHERE id=$1`, manual.ID).Scan(&running) == nil && running
	})
	queued, err := jobs.Enqueue(t.Context(), pool, cfg, "job/root", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	busy, err := jobs.RunExecution(t.Context(), pool, cfg, root, "execution/"+queued.ID)
	if err == nil || busy.Status != "queued" {
		t.Fatalf("concurrent job claimed: %+v %v", busy, err)
	}
	var deferred bool
	if err := pool.QueryRow(t.Context(), `SELECT available_at>clock_timestamp() AND NOT EXISTS(SELECT FROM ops.attempts WHERE execution_id=$1) FROM ops.executions WHERE id=$1`, queued.ID).Scan(&deferred); err != nil || !deferred {
		t.Fatalf("busy loop/attempt: %v %v", deferred, err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET dispatch='scheduler' WHERE id=$1`, queued.ID); err != nil {
		t.Fatal(err)
	}
	stop := startScheduler(t, pool, cfg, root)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		var finished bool
		return pool.QueryRow(t.Context(), `SELECT status='succeeded' FROM ops.executions WHERE id=$1`, queued.ID).Scan(&finished) == nil && finished
	})
	stop()
	var ordered bool
	if err := pool.QueryRow(t.Context(), `SELECT a.finished_at<=b.started_at FROM ops.executions a,ops.executions b WHERE a.id=$1 AND b.id=$2`, manual.ID, queued.ID).Scan(&ordered); err != nil || !ordered {
		t.Fatalf("manual/scheduler overlap: %v %v", ordered, err)
	}
}

func TestScheduledModelUsesNativeRefresh(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "report", `SELECT 7::integer AS id`)
	delete(cfg.Jobs, "report")
	model := cfg.Models["mart.report"]
	model.Schedule = "* * * * *"
	cfg.Models["mart.report"] = model
	stop := startScheduler(t, pool, cfg, root)
	waitFor(t, func() bool {
		var finished bool
		return pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT FROM ops.executions WHERE job_ref='model/mart.report' AND status='succeeded')`).Scan(&finished) == nil && finished
	})
	stop()
	var id int
	if err := pool.QueryRow(t.Context(), `SELECT id FROM mart.report`).Scan(&id); err != nil || id != 7 {
		t.Fatalf("scheduled model result: %d %v", id, err)
	}
}
