package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBackfillQueuesIndependentHistoricalChains(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id`)
	addModelJob(t, cfg, root, "child", `SELECT 1::integer AS id`, "job/root")
	job := cfg.Jobs["root"]
	job.Schedule = "0 * * * *"
	cfg.Jobs["root"] = job
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	preview, err := BackfillPlan(cfg, "job/root", from, from.Add(time.Hour), true)
	if err != nil || preview.Executions != 4 {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	result, err := Backfill(t.Context(), pool, cfg, "job/root", from, from.Add(time.Hour), true, nil)
	if err != nil || result.Queued != 4 {
		t.Fatalf("backfill: %+v %v", result, err)
	}
	var runs, chains, ticks int
	var sameTime bool
	if err := pool.QueryRow(t.Context(), `SELECT count(*),count(DISTINCT chain_id),(SELECT count(*) FROM ops.ticks) FROM ops.executions`).Scan(&runs, &chains, &ticks); err != nil || runs != 4 || chains != 2 || ticks != 0 {
		t.Fatalf("chain identity: %d %d %d %v", runs, chains, ticks, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT bool_and(child.scheduled_at=parent.scheduled_at) FROM ops.executions child JOIN ops.executions parent ON parent.id=ANY(child.depends_on)`).Scan(&sameTime); err != nil || !sameTime {
		t.Fatalf("historical context: %v %v", sameTime, err)
	}
	// Applying the same historical window is an explicit rerun with new identities.
	repeated, err := Backfill(t.Context(), pool, cfg, "job/root", from, from, false, nil)
	if err != nil || repeated.Queued != 1 {
		t.Fatalf("named-only replay: %+v %v", repeated, err)
	}
	job.Schedule = ""
	cfg.Jobs["root"] = job
	stop := startScheduler(t, pool, cfg, root)
	waitFor(t, func() bool {
		var n int
		return pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.executions WHERE job_ref IN ('job/root','job/child') AND status='succeeded'`).Scan(&n) == nil && n == 5
	})
	stop()
	var audited bool
	if err := pool.QueryRow(t.Context(), `SELECT count(*)=2 AND bool_and(principal=session_user) FROM ddp.audit WHERE action='jobs.backfill'`).Scan(&audited); err != nil || !audited {
		t.Fatalf("authenticated audit: %v %v", audited, err)
	}
}

func TestPauseResumeDoesNotReplayOfflineTicks(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id`)
	job := cfg.Jobs["root"]
	job.Schedule = "* * * * *"
	cfg.Jobs["root"] = job
	if _, err := SetPaused(t.Context(), pool, cfg, "job/root", true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	if _, err := pool.Exec(t.Context(), `UPDATE ops.planner_state SET planned_through=$1`, now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM ops.ticks`); err != nil {
		t.Fatal(err)
	}
	if _, err := QueueRun(t.Context(), pool, cfg, "job/root", false, nil); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("paused run accepted: %v", err)
	}
	if _, err := SetPaused(t.Context(), pool, cfg, "job/root", false); err != nil {
		t.Fatal(err)
	}
	if err := Plan(t.Context(), pool, cfg, now); err != nil {
		t.Fatal(err)
	}
	var skipped, runs int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ops.ticks WHERE job_ref='job/root' AND status='skipped' AND reason='job paused'),(SELECT count(*) FROM ops.executions WHERE job_ref='job/root')`).Scan(&skipped, &runs); err != nil || skipped < 5 || runs != 0 {
		t.Fatalf("offline pause: %d %d %v", skipped, runs, err)
	}
	if _, err := QueueRun(t.Context(), pool, cfg, "job/root", false, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCancelActiveAttemptStopsChainAndDoesNotRetry(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	cfg.Scheduler.Defaults.Timeout = "30s"
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id FROM pg_sleep(20)`)
	addModelJob(t, cfg, root, "child", `SELECT 1::integer AS id`, "job/root")
	queued, err := QueueRun(t.Context(), pool, cfg, "job/root", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := startScheduler(t, pool, cfg, root)
	waitFor(t, func() bool {
		var running bool
		return pool.QueryRow(t.Context(), `SELECT status='running' FROM ops.executions WHERE id=$1`, queued.ExecutionID).Scan(&running) == nil && running
	})
	if _, err := SetPaused(t.Context(), pool, cfg, "job/root", true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(350 * time.Millisecond)
	var stillRunning bool
	if err := pool.QueryRow(t.Context(), `SELECT status='running' AND cancel_requested_at IS NULL FROM ops.executions WHERE id=$1`, queued.ExecutionID).Scan(&stillRunning); err != nil || !stillRunning {
		t.Fatalf("pause interrupted active work: %v %v", stillRunning, err)
	}
	cancelled, err := Cancel(t.Context(), pool, "execution/"+queued.ExecutionID)
	if err != nil || !cancelled.Requested || cancelled.Status != "running" {
		t.Fatalf("cancel request: %+v %v", cancelled, err)
	}
	waitFor(t, func() bool {
		var interrupted bool
		return pool.QueryRow(t.Context(), `SELECT status='interrupted' FROM ops.executions WHERE id=$1`, queued.ExecutionID).Scan(&interrupted) == nil && interrupted
	})
	waitFor(t, func() bool {
		var skipped bool
		return pool.QueryRow(t.Context(), `SELECT status='skipped' FROM ops.executions WHERE job_ref='job/child'`).Scan(&skipped) == nil && skipped
	})
	stop()
	if recovered, err := jobs.RecoverScheduler(t.Context(), pool); err != nil || recovered != 0 {
		t.Fatalf("operator cancellation recovered: %d %v", recovered, err)
	}
	var attempts int
	var absent bool
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ops.attempts a JOIN ops.executions e ON e.id=a.execution_id WHERE e.job_ref IN ('job/root','job/child')),to_regclass('mart.root') IS NULL`).Scan(&attempts, &absent); err != nil || attempts != 1 || !absent {
		t.Fatalf("cancelled work committed/retried: %d %v %v", attempts, absent, err)
	}
}

func TestOperationAuditFailureRollsBackAndReadonlyCannotWrite(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id`)
	queued, err := QueueRun(t.Context(), pool, cfg, "job/root", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `ALTER TABLE ddp.audit ADD CONSTRAINT reject_cancel CHECK(action <> 'runs.cancel')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Cancel(t.Context(), pool, "execution/"+queued.ExecutionID); err == nil {
		t.Fatal("cancellation committed without audit")
	}
	var unchanged bool
	if err := pool.QueryRow(t.Context(), `SELECT status='queued' AND cancel_requested_at IS NULL FROM ops.executions WHERE id=$1`, queued.ExecutionID).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("partial cancellation: %v %v", unchanged, err)
	}
	if _, err := pool.Exec(t.Context(), `ALTER TABLE ddp.audit DROP CONSTRAINT reject_cancel`); err != nil {
		t.Fatal(err)
	}
	pc := pool.Config().Copy()
	pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE ddp_readonly`)
		return err
	}
	readonly, err := pgxpool.NewWithConfig(t.Context(), pc)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if _, err := SetPaused(t.Context(), readonly, cfg, "job/root", true); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("readonly pause: %v", err)
	}
	if _, err := QueueRun(t.Context(), readonly, cfg, "job/root", false, nil); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("readonly run: %v", err)
	}
	if _, err := Cancel(t.Context(), readonly, "execution/"+queued.ExecutionID); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("readonly cancel: %v", err)
	}
	cancelled, err := Cancel(t.Context(), pool, "execution/"+queued.ExecutionID)
	if err != nil || cancelled.Status != "interrupted" {
		t.Fatalf("queued cancel: %+v %v", cancelled, err)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.attempts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cancel started attempt: %d %v", n, err)
	}
}

func TestLostJobLockStopsWorkBeforeRetry(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	cfg.Scheduler.Defaults.Timeout = "30s"
	addModelJob(t, cfg, root, "root", `SELECT 1::integer AS id FROM pg_sleep(20)`)
	run, err := jobs.Enqueue(t.Context(), pool, cfg, "job/root", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan jobs.Execution, 1)
	go func() {
		result, _ := jobs.RunExecution(t.Context(), pool, cfg, root, "execution/"+run.ID)
		done <- result
	}()
	waitFor(t, func() bool {
		var running bool
		return pool.QueryRow(t.Context(), `SELECT status='running' FROM ops.executions WHERE id=$1`, run.ID).Scan(&running) == nil && running
	})
	var killed bool
	if err := pool.QueryRow(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=((hashtextextended('ddp:job:job/root',0)>>32)&4294967295)::oid AND objid=(hashtextextended('ddp:job:job/root',0)&4294967295)::oid`).Scan(&killed); err != nil || !killed {
		t.Fatalf("terminate lock session: %v %v", killed, err)
	}
	select {
	case result := <-done:
		if result.Status != "queued" {
			t.Fatalf("lost ownership outcome: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("work continued after losing its lock")
	}
	var absent bool
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('mart.root') IS NULL`).Scan(&absent); err != nil || !absent {
		t.Fatalf("unowned work committed: %v %v", absent, err)
	}
	if err := os.WriteFile(filepath.Join(root, "root.sql"), []byte(`SELECT 1::integer AS id`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET available_at=now() WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := jobs.RunExecution(t.Context(), pool, cfg, root, "execution/"+run.ID)
	if err != nil || retry.Status != "succeeded" {
		t.Fatalf("reacquired retry: %+v %v", retry, err)
	}
}
