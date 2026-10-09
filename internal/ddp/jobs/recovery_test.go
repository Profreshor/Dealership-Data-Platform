package jobs

import (
	"testing"
	"time"
)

func TestRecoverSchedulerRequeuesAndIsIdempotent(t *testing.T) {
	pool, root := newModelJobTestDB(t)
	defer pool.Close()
	ctx := t.Context()
	cfg := modelJobConfig("missing.sql")
	cfg.Scheduler.Defaults.Retry.MaxAttempts = 2
	cfg.Scheduler.Defaults.Retry.InitialDelay = "1s"
	cfg.Scheduler.Defaults.Retry.MaxDelay = "1s"
	run, err := Enqueue(ctx, pool, cfg, "job/refresh", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.executions SET dispatch='scheduler',status='running' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.attempts(id,execution_id,number,status) VALUES('recovery-attempt-1',$1,1,'running')`, run.ID); err != nil {
		t.Fatal(err)
	}
	// The saved execution policy must win even if the current config changes.
	cfg.Scheduler.Defaults.Retry.MaxAttempts = 1
	count, err := RecoverScheduler(ctx, pool)
	if err != nil || count != 1 {
		t.Fatalf("recovery: %d %v", count, err)
	}
	var status, attemptStatus, attemptError string
	var reason *string
	var attempts, events int
	if err := pool.QueryRow(ctx, `SELECT e.status,e.reason,a.status,a.error,(SELECT count(*) FROM ops.attempts WHERE execution_id=e.id),(SELECT count(*) FROM ops.events WHERE resource_ref='execution/'||e.id) FROM ops.executions e JOIN ops.attempts a ON a.execution_id=e.id WHERE e.id=$1`, run.ID).Scan(&status, &reason, &attemptStatus, &attemptError, &attempts, &events); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || reason != nil || attemptStatus != "interrupted" || attemptError != recoveryAttemptError || attempts != 1 || events != 1 {
		t.Fatalf("reconciled state: %q %v %q %q attempts=%d events=%d", status, reason, attemptStatus, attemptError, attempts, events)
	}
	if count, err := RecoverScheduler(ctx, pool); err != nil || count != 0 {
		t.Fatalf("second recovery: %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.executions SET available_at=now() WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := RunExecution(ctx, pool, cfg, root, "execution/"+run.ID); err == nil {
		t.Fatal("missing model unexpectedly succeeded")
	}
	if err := pool.QueryRow(ctx, `SELECT number FROM ops.attempts WHERE execution_id=$1 ORDER BY number DESC LIMIT 1`, run.ID).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("retry attempt number: %d %v", attempts, err)
	}
}

func TestRecoverSchedulerIgnoresManualAndOperatorInterruptions(t *testing.T) {
	pool, _ := newModelJobTestDB(t)
	defer pool.Close()
	ctx := t.Context()
	cfg := modelJobConfig("missing.sql")
	for _, tc := range []struct {
		name, dispatch, status, reason string
	}{
		{"manual", "manual", "running", ""},
		{"operator", "scheduler", "interrupted", "operator interrupted"},
	} {
		run, err := Enqueue(ctx, pool, cfg, "job/refresh", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE ops.executions SET dispatch=$2,status=$3,reason=$4 WHERE id=$1`, run.ID, tc.dispatch, tc.status, tc.reason); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := RecoverScheduler(ctx, pool); err != nil || count != 0 {
		t.Fatalf("ignored executions: %d %v", count, err)
	}
}

func TestRecoverSchedulerExhaustedExecutionFails(t *testing.T) {
	pool, _ := newModelJobTestDB(t)
	defer pool.Close()
	ctx := t.Context()
	cfg := modelJobConfig("missing.sql")
	run, err := Enqueue(ctx, pool, cfg, "job/refresh", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.executions SET dispatch='scheduler',status='running' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.attempts(id,execution_id,number,status) VALUES('exhausted-attempt-1',$1,1,'running')`, run.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := RecoverScheduler(ctx, pool); err != nil || count != 1 {
		t.Fatalf("recovery: %d %v", count, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM ops.executions WHERE id=$1`, run.ID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("exhausted execution: %q %v", status, err)
	}
}

func TestRecoveryEventFailureRollsBack(t *testing.T) {
	pool, _ := newModelJobTestDB(t)
	defer pool.Close()
	ctx := t.Context()
	cfg := modelJobConfig("missing.sql")
	run, err := Enqueue(ctx, pool, cfg, "job/refresh", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.executions SET dispatch='scheduler',status='running' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.attempts(id,execution_id,number,status) VALUES('rollback-attempt',$1,1,'running')`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE ops.events ADD CONSTRAINT reject_recovery CHECK(kind <> 'recovered')`); err != nil {
		t.Fatal(err)
	}
	if count, err := RecoverScheduler(ctx, pool); err == nil || count != 0 {
		t.Fatalf("recovery accepted broken audit: %d %v", count, err)
	}
	var execution, attempt string
	if err := pool.QueryRow(ctx, `SELECT e.status,a.status FROM ops.executions e JOIN ops.attempts a ON a.execution_id=e.id WHERE e.id=$1`, run.ID).Scan(&execution, &attempt); err != nil || execution != "running" || attempt != "running" {
		t.Fatalf("partial recovery: %s %s %v", execution, attempt, err)
	}
}

func TestRecoveryHonorsCancellationRequestedBeforeCrash(t *testing.T) {
	pool, _ := newModelJobTestDB(t)
	defer pool.Close()
	cfg := modelJobConfig("missing.sql")
	cfg.Scheduler.Defaults.Retry.MaxAttempts = 3
	run, err := Enqueue(t.Context(), pool, cfg, "job/refresh", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET dispatch='scheduler',status='running',cancel_requested_at=clock_timestamp(),reason='operator interrupted' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO ops.attempts(id,execution_id,number,status) VALUES('cancel-crash-attempt',$1,1,'running')`, run.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := RecoverScheduler(t.Context(), pool); err != nil || count != 1 {
		t.Fatalf("recovery: %d %v", count, err)
	}
	var status, reason, attempt, attemptError string
	if err := pool.QueryRow(t.Context(), `SELECT e.status,e.reason,a.status,a.error FROM ops.executions e JOIN ops.attempts a ON a.execution_id=e.id WHERE e.id=$1`, run.ID).Scan(&status, &reason, &attempt, &attemptError); err != nil || status != "interrupted" || reason != "operator interrupted" || attempt != "interrupted" || attemptError != ErrOperatorCancelled.Error() {
		t.Fatalf("cancelled crash: %q %q %q %q %v", status, reason, attempt, attemptError, err)
	}
	if count, err := RecoverScheduler(t.Context(), pool); err != nil || count != 0 {
		t.Fatalf("cancelled run retried: %d %v", count, err)
	}
}
