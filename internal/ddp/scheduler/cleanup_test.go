package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
)

func TestCleanupTickRunsDurablyAndRefusesBackfill(t *testing.T) {
	pool := schedulerDB(t)
	cfg := schedulerConfig()
	point := time.Now().UTC().Truncate(time.Hour)
	if _, err := BackfillPlan(cfg, jobs.CleanupRef, point, point, false); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("cleanup backfill: %v", err)
	}
	if err := Plan(t.Context(), pool, cfg, point); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(t.Context(), `SELECT id FROM ops.executions WHERE job_ref=$1`, jobs.CleanupRef).Scan(&id); err != nil {
		t.Fatal(err)
	}
	startScheduler(t, pool, cfg, t.TempDir())
	waitFor(t, func() bool {
		var status string
		return pool.QueryRow(t.Context(), `SELECT status FROM ops.executions WHERE id=$1`, id).Scan(&status) == nil && status == "succeeded"
	})
	var attempts int
	var more bool
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.attempts WHERE execution_id=$1`, id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("cleanup attempts=%d err=%v", attempts, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT (result->'details'->'expired'->>'more')::boolean FROM ops.attempts WHERE execution_id=$1`, id).Scan(&more); err != nil || more {
		t.Fatalf("cleanup result more=%t err=%v", more, err)
	}
}
