package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
)

func TestSchedulerRestartRecoversInterruptedAttempt(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "parent", `SELECT 1::integer AS id FROM pg_sleep(30)`)
	addModelJob(t, cfg, root, "child", `SELECT 1::integer AS id`, "job/parent")
	parent := cfg.Jobs["parent"]
	parent.Retry = &config.Retry{MaxAttempts: 2, InitialDelay: "10ms", MaxDelay: "10ms"}
	parent.Timeout = "30s"
	cfg.Jobs["parent"] = parent
	parentRun, err := jobs.Enqueue(t.Context(), pool, cfg, "job/parent", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	childRun, err := jobs.Enqueue(t.Context(), pool, cfg, "job/child", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET dispatch='scheduler' WHERE id=$1`, parentRun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET dispatch='scheduler',status='waiting',depends_on=ARRAY[$2]::text[],chain_id=$2 WHERE id=$1`, childRun.ID, parentRun.ID); err != nil {
		t.Fatal(err)
	}

	stop := startScheduler(t, pool, cfg, root)
	waitFor(t, func() bool {
		var status string
		return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, parentRun.ID).Scan(&status) == nil && status == "running"
	})
	stop()

	var executionStatus, reason, attemptStatus string
	if err := pool.QueryRow(t.Context(), `SELECT e.status,e.reason,a.status FROM ops.executions e JOIN ops.attempts a ON a.execution_id=e.id WHERE e.id=$1 ORDER BY a.number DESC LIMIT 1`, parentRun.ID).Scan(&executionStatus, &reason, &attemptStatus); err != nil {
		t.Fatal(err)
	}
	if executionStatus != "interrupted" || reason != "scheduler shutdown" || attemptStatus != "interrupted" {
		t.Fatalf("shutdown outcome: status=%q reason=%q attempt=%q", executionStatus, reason, attemptStatus)
	}

	if err := os.WriteFile(filepath.Join(root, "parent.sql"), []byte(`SELECT 1::integer AS id`), 0600); err != nil {
		t.Fatal(err)
	}
	// The saved execution policy must win over this changed registry policy.
	parent.Retry = &config.Retry{MaxAttempts: 1, InitialDelay: "10ms", MaxDelay: "10ms"}
	cfg.Jobs["parent"] = parent
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET available_at=now() WHERE id=$1`, parentRun.ID); err != nil {
		t.Fatal(err)
	}
	startScheduler(t, pool, cfg, root)
	waitFor(t, func() bool {
		var status string
		return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, childRun.ID).Scan(&status) == nil && status == "succeeded"
	})

	var attempts, succeeded int
	if err := pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE status='succeeded') FROM ops.attempts WHERE execution_id=$1`, parentRun.ID).Scan(&attempts, &succeeded); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || succeeded != 1 {
		t.Fatalf("recovery attempts: total=%d succeeded=%d", attempts, succeeded)
	}
	var ordered bool
	if err := pool.QueryRow(t.Context(), `SELECT child.started_at>=parent.finished_at FROM ops.executions child JOIN ops.executions parent ON parent.id=$2 WHERE child.id=$1`, childRun.ID, parentRun.ID).Scan(&ordered); err != nil || !ordered {
		t.Fatalf("child ordering: %v %v", ordered, err)
	}
}

func TestSchedulerTimeoutExhaustsRetriesAndSkipsChild(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	root := t.TempDir()
	addModelJob(t, cfg, root, "parent", `SELECT 1::integer AS id FROM pg_sleep(1)`)
	addModelJob(t, cfg, root, "child", `SELECT 1::integer AS id`, "job/parent")
	parent := cfg.Jobs["parent"]
	parent.Timeout = "100ms"
	parent.Retry = &config.Retry{MaxAttempts: 2, InitialDelay: "10ms", MaxDelay: "10ms"}
	cfg.Jobs["parent"] = parent
	parentRun, err := jobs.Enqueue(t.Context(), pool, cfg, "job/parent", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	childRun, err := jobs.Enqueue(t.Context(), pool, cfg, "job/child", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET dispatch='scheduler' WHERE id=$1`, parentRun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET dispatch='scheduler',status='waiting',depends_on=ARRAY[$2]::text[],chain_id=$2 WHERE id=$1`, childRun.ID, parentRun.ID); err != nil {
		t.Fatal(err)
	}
	// Prove the retry count is read from the enqueued execution, not current config.
	parent.Retry = &config.Retry{MaxAttempts: 1, InitialDelay: "10ms", MaxDelay: "10ms"}
	cfg.Jobs["parent"] = parent
	stop := startScheduler(t, pool, cfg, root)
	waitFor(t, func() bool {
		var status string
		return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, childRun.ID).Scan(&status) == nil && status == "skipped"
	})
	stop()

	var attempts, failed int
	if err := pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE status='failed') FROM ops.attempts WHERE execution_id=$1`, parentRun.ID).Scan(&attempts, &failed); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || failed != 2 {
		t.Fatalf("timeout attempts: total=%d failed=%d", attempts, failed)
	}
	var childStatus string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, childRun.ID).Scan(&childStatus); err != nil || childStatus != "skipped" {
		t.Fatalf("child status: %q %v", childStatus, err)
	}
}
