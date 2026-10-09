package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPythonIngestAgainstPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	name := fmt.Sprintf("ddp_jobs_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := migrate.Up(ctx, conn.Conn()); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	project, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile(filepath.Join(project, "internal/ddp/testdata/reporting/migrations/app/20260904030000_customers.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(schema)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, path := range []string{"jobs/sync_customers.py", "client/synthetic.py"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(path)), 0700); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(project, "internal/ddp/testdata/reporting", path))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"ddp", ".venv"} {
		if err := os.Symlink(filepath.Join(project, path), filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".venv/bin/python")); err != nil {
		t.Fatal("run make setup before subprocess tests:", err)
	}
	var since string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/customers" || r.Header.Get("X-API-Key") != "synthetic-secret" {
			http.Error(w, "bad request", 400)
			return
		}
		since = r.URL.Query().Get("since")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"id":"one","name":"Synthetic Customer","updated_at":"2026-09-04T12:00:00Z"}]`)
	}))
	defer server.Close()
	cfg, err := config.Load(filepath.Join(project, "internal/ddp/testdata/base/ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Integrations["synthetic"] = config.Integration{Kind: "http", BaseURL: server.URL, Auth: &config.IntegrationAuth{Type: "api_key", Header: "X-API-Key", Secret: "SYNTHETIC_API_KEY"}}
	cfg.Jobs["sync_customers"] = config.Job{Purpose: "Seed synthetic customers", Action: "ingest", Python: "jobs.sync_customers", Deletions: "ignore", Reads: []string{"integration/synthetic"}, Writes: []config.Write{{Target: "table/synthetic.customers", Mode: "upsert", Key: []string{"id"}}}}
	// This URL points only at the temporary test database, never a production role.
	childURL, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	childURL.Database = name
	// libpq accepts the original URL with an explicit dbname connection parameter.
	t.Setenv("JOB_DATABASE_URL", fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable", childURL.Host, childURL.Port, childURL.User, childURL.Password, name))
	t.Setenv("SYNTHETIC_API_KEY", "synthetic-secret")
	t.Setenv("UNDECLARED_SECRET", "must-not-reach-child")
	for i := 0; i < 2; i++ {
		run, err := Run(ctx, pool, cfg, root, "job/sync_customers")
		if err != nil {
			t.Fatalf("%+v: %v", run, err)
		}
		if run.Status != "succeeded" {
			t.Fatal(run)
		}
	}
	if since != "2026-09-04T12:00:00Z" {
		t.Fatal("watermark not passed on replay", since)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM synthetic.customers").Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay duplicated data: %d %v", count, err)
	}
	job := cfg.Jobs["sync_customers"]
	job.Python = "jobs.failure"
	cfg.Jobs["sync_customers"] = job
	code := "import os\ndef run(ctx):\n    assert 'UNDECLARED_SECRET' not in os.environ\n    print(os.environ['SYNTHETIC_API_KEY'])\n    print('x' * 100000)\n    raise RuntimeError('expected failure')\n"
	if err := os.WriteFile(filepath.Join(root, "jobs/failure.py"), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := Run(ctx, pool, cfg, root, "job/sync_customers")
	if err == nil || run.Status != "failed" {
		t.Fatalf("failed job accepted: %+v %v", run, err)
	}
	var log string
	if err := pool.QueryRow(ctx, "SELECT stdout FROM ops.attempts WHERE id=$1", run.AttemptID).Scan(&log); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log, "synthetic-secret") || !strings.Contains(log, "[REDACTED]") || !strings.Contains(log, "[output truncated]") {
		t.Fatal("logs were not bounded and redacted")
	}
	var watermark string
	if err := pool.QueryRow(ctx, "SELECT value #>> '{}' FROM ops.watermarks WHERE job_ref='job/sync_customers'").Scan(&watermark); err != nil || watermark != "2026-09-04T12:00:00Z" {
		t.Fatalf("failed job changed watermark: %s %v", watermark, err)
	}
	job.Timeout = "100ms"
	cfg.Jobs["sync_customers"] = job
	if err := os.WriteFile(filepath.Join(root, "jobs/failure.py"), []byte("import time\ndef run(ctx):\n    time.sleep(30)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	run, err = Run(ctx, pool, cfg, root, "job/sync_customers")
	if err == nil || run.Status != "failed" || time.Since(start) > 5*time.Second {
		t.Fatalf("deadline not enforced: %+v %v", run, err)
	}
}

func TestIntegrationCannotOverrideRuntimeEnvironment(t *testing.T) {
	t.Setenv("JOB_DATABASE_URL", "synthetic-test-database")
	for _, secret := range []string{"PATH", "DATABASE_URL", "PYTHONUNBUFFERED", "PYTHONDONTWRITEBYTECODE", "LANG", "BACKUP_DATABASE_URL", "BACKUP_ACCESS_KEY_ID", "BACKUP_SECRET_ACCESS_KEY", "BACKUP_AGE_IDENTITY"} {
		cfg := &config.Config{
			Jobs:         map[string]config.Job{"test": {Python: "jobs.test", Timeout: "1s", Reads: []string{"integration/test"}}},
			Integrations: map[string]config.Integration{"test": {Auth: &config.IntegrationAuth{Secret: secret}}},
		}
		if _, err := Run(t.Context(), nil, cfg, t.TempDir(), "job/test"); err == nil || !strings.Contains(err.Error(), "conflicts with the job runtime") {
			t.Fatalf("runtime override %s accepted: %v", secret, err)
		}
	}
}

func TestModelJobUsesGoRefreshAndDurableOutcome(t *testing.T) {
	p, root := newModelJobTestDB(t)
	defer p.Close()
	if _, err := p.Exec(t.Context(), `CREATE TABLE staging.source (id integer PRIMARY KEY); INSERT INTO staging.source VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models/mart"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models/mart/result.sql"), []byte("SELECT id FROM staging.source"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := modelJobConfig("models/mart/result.sql")
	t.Setenv("JOB_DATABASE_URL", "")
	cfg.Integrations["secret"] = config.Integration{Auth: &config.IntegrationAuth{Secret: "MUST_NOT_BE_READ"}}
	job := cfg.Jobs["refresh"]
	job.Reads = []string{"integration/secret"}
	cfg.Jobs["refresh"] = job
	run, err := Run(t.Context(), p, cfg, root, "job/refresh")
	if err != nil || run.Status != "succeeded" {
		t.Fatalf("model job failed: %+v: %v", run, err)
	}
	var attemptStatus, modelStatus, details string
	if err := p.QueryRow(t.Context(), `SELECT a.status, (SELECT status FROM ops.model_refreshes), a.result->'details'->>'model_ref' FROM ops.attempts a WHERE a.id=$1`, run.AttemptID).Scan(&attemptStatus, &modelStatus, &details); err != nil {
		t.Fatal(err)
	}
	if attemptStatus != "succeeded" || modelStatus != "succeeded" || details != "model/mart.result" {
		t.Fatalf("unexpected durable model outcome: %s %s %s", attemptStatus, modelStatus, details)
	}
	var watermarks int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM ops.watermarks WHERE job_ref='job/refresh'`).Scan(&watermarks); err != nil || watermarks != 0 {
		t.Fatalf("model job wrote a watermark: %d %v", watermarks, err)
	}
	if _, err := p.Exec(t.Context(), `DROP VIEW mart.result`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models/mart/result.sql"), []byte("SELECT missing FROM staging.source"), 0600); err != nil {
		t.Fatal(err)
	}
	failed, err := Run(t.Context(), p, cfg, root, "job/refresh")
	if err == nil || failed.Status != "failed" {
		t.Fatalf("failed model job accepted: %+v: %v", failed, err)
	}
	var failedAttempt, failedModel int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE status='failed'), (SELECT count(*) FROM ops.model_refreshes WHERE status='failed') FROM ops.attempts WHERE id=$1`, failed.AttemptID).Scan(&failedAttempt, &failedModel); err != nil {
		t.Fatal(err)
	}
	if failedAttempt != 1 || failedModel != 1 {
		t.Fatalf("failure was not recorded: attempts=%d models=%d", failedAttempt, failedModel)
	}
	if _, err := Run(t.Context(), p, cfg, root, "job/missing"); err == nil || !strings.Contains(err.Error(), "unknown job") {
		t.Fatal("unknown job was accepted")
	}
	cfg.Jobs["unknown_model"] = config.Job{Model: "model/mart/missing", Timeout: "1s"}
	if _, err := Run(t.Context(), p, cfg, root, "job/unknown_model"); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatal("unknown model was accepted")
	}
}

func TestModelJobCancellationFinishesAttempt(t *testing.T) {
	p, root := newModelJobTestDB(t)
	defer p.Close()
	if _, err := p.Exec(t.Context(), `CREATE TABLE staging.source (id integer PRIMARY KEY); INSERT INTO staging.source VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models/mart"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models/mart/slow.sql"), []byte("SELECT id FROM staging.source"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := modelJobConfig("models/mart/slow.sql")
	job := cfg.Jobs["refresh"]
	job.Timeout = "5s"
	cfg.Jobs["refresh"] = job
	if _, err := Run(t.Context(), p, cfg, root, "job/refresh"); err != nil {
		t.Fatal(err)
	}
	conn, err := p.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	lock, err := conn.Begin(t.Context())
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if _, err := lock.Exec(t.Context(), `LOCK TABLE mart.result IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = lock.Rollback(t.Context())
		conn.Release()
		t.Fatal(err)
	}
	defer conn.Release()
	defer lock.Rollback(context.Background())
	for _, test := range []struct {
		name, timeout, status string
		cancelParent          bool
	}{
		{"timeout", "100ms", "failed", false},
		{"cancellation", "5s", "interrupted", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			job.Timeout = test.timeout
			cfg.Jobs["refresh"] = job
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := time.Now()
			var run Execution
			var err error
			if test.cancelParent {
				done := make(chan struct{})
				go func() { run, err = Run(ctx, p, cfg, root, "job/refresh"); close(done) }()
				// Cancel the actual blocked refresh, not an arbitrary startup delay.
				deadline := time.Now().Add(3 * time.Second)
				blocked := false
				for time.Now().Before(deadline) {
					if err := p.QueryRow(t.Context(), `SELECT EXISTS(SELECT FROM pg_locks WHERE relation='mart.result'::regclass AND NOT granted)`).Scan(&blocked); err != nil {
						t.Fatal(err)
					}
					if blocked {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
				<-done
				if !blocked {
					t.Fatal("model never reached its blocked refresh")
				}
			} else {
				run, err = Run(ctx, p, cfg, root, "job/refresh")
			}
			if err == nil || run.Status != test.status || time.Since(started) > 3*time.Second {
				t.Fatalf("bounded stop not reported: %+v: %v", run, err)
			}
			var attemptStatus, executionStatus, modelStatus string
			if err := p.QueryRow(t.Context(), `SELECT a.status,e.status,(SELECT status FROM ops.model_refreshes ORDER BY started_at DESC LIMIT 1) FROM ops.attempts a JOIN ops.executions e ON e.id=a.execution_id WHERE a.id=$1`, run.AttemptID).Scan(&attemptStatus, &executionStatus, &modelStatus); err != nil {
				t.Fatal(err)
			}
			if attemptStatus != test.status || executionStatus != test.status || modelStatus != "failed" {
				t.Fatalf("incomplete durable outcomes: %s %s %s", attemptStatus, executionStatus, modelStatus)
			}
		})
	}

}

func modelJobConfig(file string) *config.Config {
	return &config.Config{
		Ddp:          config.Identity{Timezone: "UTC"},
		Scheduler:    config.Scheduler{Defaults: config.Defaults{Timeout: "1s", Retry: config.Retry{MaxAttempts: 1, InitialDelay: "1ms", MaxDelay: "1ms"}}},
		Integrations: map[string]config.Integration{},
		Jobs:         map[string]config.Job{"refresh": {Purpose: "Refresh model", Action: "transform", Model: "model/mart.result", Timeout: "1s"}},
		Models:       map[string]config.Model{"mart.result": {File: file, Purpose: "Result", Materialization: "view", Reads: []string{"table/staging.source"}, Contract: config.Contract{Columns: map[string]config.Column{"id": {Type: "integer", Nullable: false}}}}},
	}
}

func newModelJobTestDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ddp_model_jobs_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(context.Background())
	})
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = name
	p, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := p.Acquire(ctx)
	if err != nil {
		p.Close()
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		p.Close()
		t.Fatal(err)
	}
	conn.Release()
	return p, t.TempDir()
}

func TestResultBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	for _, raw := range []string{`null`, `{"rows_read":-1}`, `{"rows_read":true}`, `{"error":{}}`, `{"warnings":[42]}`, `{} {}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readResult(path); err == nil {
			t.Fatal("accepted invalid result", raw)
		}
	}
	if err := os.WriteFile(path, []byte(`{"rows_written":0,"watermark":"today"}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := readResult(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsWritten == nil || *result.RowsWritten != 0 || !json.Valid(result.Watermark) {
		t.Fatal(result)
	}
}

func TestParentCancellationDuringClaimKeepsDurableAttempt(t *testing.T) {
	pool, root := newModelJobTestDB(t)
	defer pool.Close()
	if err := os.MkdirAll(filepath.Join(root, "models/mart"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models/mart/claim.sql"), []byte("SELECT id FROM staging.source"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := modelJobConfig("models/mart/claim.sql")
	if _, err := pool.Exec(t.Context(), `CREATE TABLE staging.source(id integer PRIMARY KEY);
CREATE FUNCTION ops.slow_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.status='running' THEN PERFORM pg_sleep(0.3); END IF; RETURN NEW; END $$;
CREATE TRIGGER slow_claim BEFORE UPDATE ON ops.executions FOR EACH ROW EXECUTE FUNCTION ops.slow_claim()`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var run Execution
	var runErr error
	done := make(chan struct{})
	go func() { run, runErr = Run(ctx, pool, cfg, root, "job/refresh"); close(done) }()
	claiming := false
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT FROM pg_stat_activity WHERE datname=current_database() AND wait_event='PgSleep' AND query LIKE 'UPDATE ops.executions%')`).Scan(&claiming); err != nil {
			t.Fatal(err)
		}
		if claiming {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("claim did not stop")
	}
	if !claiming || !errors.Is(runErr, context.Canceled) || run.Status != "interrupted" {
		t.Fatalf("claiming=%t result=%+v err=%v", claiming, run, runErr)
	}
	var durable bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT FROM ops.attempts a JOIN ops.executions e ON e.id=a.execution_id WHERE a.id=$1 AND a.status='interrupted' AND e.status='interrupted')`, run.AttemptID).Scan(&durable); err != nil || !durable {
		t.Fatalf("claim cancellation lost its attempt: %t %v", durable, err)
	}
}
