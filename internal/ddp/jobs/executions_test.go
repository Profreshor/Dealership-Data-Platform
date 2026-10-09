package jobs

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQueuedRetryKeepsExecutionContextAndStoredPolicy(t *testing.T) {
	pool, root := newModelJobTestDB(t)
	defer pool.Close()
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `CREATE TABLE app.retry_probe(execution_id text,attempt_id text,effect_key text,scheduled_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	project, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ddp", ".venv"} {
		if err := os.Symlink(filepath.Join(project, name), filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	source := `from ddp import JobResult, db

def run(ctx):
    with db.transaction(ctx) as conn:
        conn.execute("INSERT INTO app.retry_probe VALUES (%s,%s,%s,%s)", (ctx.execution_id,ctx.attempt_id,ctx.effect_key("probe"),ctx.scheduled_at))
        count = conn.execute("SELECT count(*) FROM app.retry_probe WHERE execution_id=%s", (ctx.execution_id,)).fetchone()[0]
    if count == 1:
        raise RuntimeError("synthetic first attempt failure")
    return JobResult(watermark="complete")
`
	if err := os.WriteFile(filepath.Join(root, "jobs/retry.py"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := modelJobConfig("")
	cfg.Jobs = map[string]config.Job{"probe": {Action: "check", Python: "jobs.retry"}}
	// This tests retry identity, not startup speed under parallel test load.
	// The subprocess test in run_test.go covers the workload deadline separately.
	cfg.Scheduler.Defaults.Timeout = "30s"
	// Keep the retry beyond the test's runtime; CI may take longer than a short
	// backoff between assertions. The due time is advanced explicitly below.
	cfg.Scheduler.Defaults.Retry = config.Retry{MaxAttempts: 2, InitialDelay: "1h", MaxDelay: "1h"}
	childURL, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	childURL.Path = "/" + pool.Config().ConnConfig.Database
	t.Setenv("JOB_DATABASE_URL", childURL.String())
	scheduled := time.Date(2026, 9, 1, 9, 15, 0, 0, time.UTC)
	queued, err := Enqueue(ctx, pool, cfg, "job/probe", scheduled)
	if err != nil {
		t.Fatal(err)
	}
	var started *time.Time
	var count int
	if err := pool.QueryRow(ctx, `SELECT started_at,(SELECT count(*) FROM ops.attempts) FROM ops.executions WHERE id=$1`, queued.ID).Scan(&started, &count); err != nil || started != nil || count != 0 {
		t.Fatalf("enqueue launched work: %v %d %v", started, count, err)
	}
	cfg.Scheduler.Defaults.Retry = config.Retry{MaxAttempts: 1, InitialDelay: "2h", MaxDelay: "2h"}
	first, err := RunExecution(ctx, pool, cfg, root, "execution/"+queued.ID)
	if err == nil || first.Status != "queued" || first.ID != queued.ID {
		t.Fatalf("first attempt: %+v %v", first, err)
	}
	var firstRows int
	queryErr := pool.QueryRow(ctx, `SELECT count(*) FROM app.retry_probe WHERE execution_id=$1`, first.ID).Scan(&firstRows)
	if queryErr != nil || firstRows != 1 {
		t.Fatalf("first attempt did not reach its deliberate failure: rows=%d work error=%v query error=%v", firstRows, err, queryErr)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.watermarks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed attempt advanced watermark: %d %v", count, err)
	}
	if _, err := RunExecution(ctx, pool, cfg, root, "execution/"+queued.ID); err == nil {
		t.Fatal("retry launched before available_at")
	}
	// Advance the stored due time explicitly; the preceding claim tested its gate.
	if _, err := pool.Exec(ctx, `UPDATE ops.executions SET available_at=now() WHERE id=$1`, queued.ID); err != nil {
		t.Fatal(err)
	}
	second, err := RunExecution(ctx, pool, cfg, root, "execution/"+queued.ID)
	if err != nil || second.Status != "succeeded" || second.ID != first.ID || second.AttemptID == first.AttemptID {
		t.Fatalf("retry identity: %+v %+v %v", first, second, err)
	}
	var executions, attempts, effects, schedules int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT execution_id),count(DISTINCT attempt_id),count(DISTINCT effect_key),count(DISTINCT scheduled_at) FROM app.retry_probe`).Scan(&executions, &attempts, &effects, &schedules); err != nil || executions != 1 || attempts != 2 || effects != 1 || schedules != 1 {
		t.Fatalf("workload identities: %d %d %d %d %v", executions, attempts, effects, schedules, err)
	}
	var actualScheduled time.Time
	var watermark string
	if err := pool.QueryRow(ctx, `SELECT min(scheduled_at),(SELECT value #>> '{}' FROM ops.watermarks WHERE job_ref='job/probe') FROM app.retry_probe`).Scan(&actualScheduled, &watermark); err != nil || !actualScheduled.Equal(scheduled) || watermark != "complete" {
		t.Fatalf("result context: %v %q %v", actualScheduled, watermark, err)
	}
	if _, err := RunExecution(ctx, pool, cfg, root, "execution/"+queued.ID); err == nil {
		t.Fatal("successful execution relaunched")
	}
	rerun, err := Run(ctx, pool, cfg, root, "job/probe")
	if err == nil || rerun.ID == queued.ID || rerun.Status != "failed" {
		t.Fatalf("new execution did not use new policy: %+v %v", rerun, err)
	}
}

func TestLosingCallerCannotDeferTheLockOwnerBeforeClaim(t *testing.T) {
	pool, root := newModelJobTestDB(t)
	defer pool.Close()
	cfg := modelJobConfig("missing.sql")
	queued, err := Enqueue(t.Context(), pool, cfg, "job/refresh", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	lockHeld, releaseClaim := make(chan struct{}), make(chan struct{})
	resumeClaim := sync.OnceFunc(func() { close(releaseClaim) })
	defer resumeClaim()
	settings := pool.Config()
	var acquisitions atomic.Int32
	settings.BeforeAcquire = func(ctx context.Context, _ *pgx.Conn) bool {
		// The first acquisition reads the job reference. Pause the transaction
		// acquisition after its dedicated session has acquired the job lock.
		if acquisitions.Add(1) == 2 {
			close(lockHeld)
			select {
			case <-releaseClaim:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	winner, err := pgxpool.NewWithConfig(t.Context(), settings)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { resumeClaim(); winner.Close() }()
	done := make(chan Execution, 1)
	go func() { run, _ := RunExecution(t.Context(), winner, cfg, root, "execution/"+queued.ID); done <- run }()
	select {
	case <-lockHeld:
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not reach claim boundary")
	}
	if _, err := RunExecution(t.Context(), pool, cfg, root, "execution/"+queued.ID); err == nil {
		t.Fatal("losing caller acquired job lock")
	}
	resumeClaim()
	var run Execution
	select {
	case run = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not finish")
	}
	var attempts int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM ops.attempts WHERE execution_id=$1", queued.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || attempts != 1 {
		t.Fatalf("lock owner lost its due execution: status=%s attempts=%d", run.Status, attempts)
	}
}

func TestExecutionClaimIsExclusiveAndPreparationFailureIsDurable(t *testing.T) {
	pool, root := newModelJobTestDB(t)
	defer pool.Close()
	cfg := modelJobConfig("missing.sql")
	queued, err := Enqueue(t.Context(), pool, cfg, "job/refresh", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, _ = RunExecution(t.Context(), pool, cfg, root, "execution/"+queued.ID) })
	}
	wg.Wait()
	var count int
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM ops.attempts WHERE execution_id=$1) FROM ops.executions WHERE id=$1`, queued.ID).Scan(&status, &count); err != nil || status != "failed" || count != 1 {
		t.Fatalf("duplicate claim or lost error: %s %d %v", status, count, err)
	}
}

func TestCancellationDuringRetryWaitStopsFutureAttempts(t *testing.T) {
	pool, root := newModelJobTestDB(t)
	defer pool.Close()
	cfg := modelJobConfig("missing.sql")
	cfg.Scheduler.Defaults.Retry = config.Retry{MaxAttempts: 3, InitialDelay: "1h", MaxDelay: "1h"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var run Execution
	var runErr error
	done := make(chan struct{})
	go func() { run, runErr = Run(ctx, pool, cfg, root, "job/refresh"); close(done) }()
	// Cancel only after the failed attempt is durably queued for retry. A short
	// deadline can expire before the first claim on a loaded test host.
	retrying := false
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT FROM ops.executions e JOIN ops.attempts a ON a.execution_id=e.id WHERE e.status='queued' AND e.available_at>clock_timestamp() AND a.status='failed')`).Scan(&retrying); err != nil {
			t.Fatal(err)
		}
		if retrying {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("retry wait did not stop")
	}
	if !retrying || !errors.Is(runErr, context.Canceled) || run.Status != "interrupted" {
		t.Fatalf("retrying=%t result=%+v err=%v", retrying, run, runErr)
	}
	var status string
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM ops.attempts WHERE execution_id=$1) FROM ops.executions WHERE id=$1`, run.ID).Scan(&status, &count); err != nil || status != "interrupted" || count != 1 {
		t.Fatalf("interrupted retry state: %s %d %v", status, count, err)
	}
}

func TestRetryDelayBounds(t *testing.T) {
	for _, attempt := range []int{1, 2, 3, 1000} {
		cap := min(time.Second*time.Duration(1<<min(attempt-1, 3)), 5*time.Second)
		for range 30 {
			delay := retryDelay(attempt, time.Second, 5*time.Second)
			if delay <= cap/2 || delay > cap {
				t.Fatalf("attempt %d delay %v outside (%v,%v]", attempt, delay, cap/2, cap)
			}
		}
	}
	if got := retryDelay(1000, time.Nanosecond, time.Nanosecond); got != time.Nanosecond {
		t.Fatal(got)
	}
}
