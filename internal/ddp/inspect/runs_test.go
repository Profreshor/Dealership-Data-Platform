package inspect

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRunsReadOnlyHistory(t *testing.T) {
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
	db := fmt.Sprintf("ddp_inspect_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)") //nolint:errcheck

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = db
	owner, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	conn, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	if _, err := owner.Exec(ctx, `INSERT INTO ops.executions (id, job_ref, scheduled_at, started_at, finished_at, status) VALUES
('01J00000000000000000000001','job/first','2026-01-01T00:00:00Z','2026-01-01T00:01:00Z','2026-01-01T00:02:00Z','succeeded'),
('01J00000000000000000000002','job/first','2026-01-02T00:00:00Z',NULL,NULL,'queued'),
('01J00000000000000000000003','model/mart.first','2026-01-03T00:00:00Z',NULL,NULL,'succeeded'),
('01J00000000000000000000004','ddp:comms_relay','2026-01-04T00:00:00Z',NULL,NULL,'failed')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO ops.attempts (id, execution_id, number, started_at, finished_at, status, stdout, stderr, result, error) VALUES
('01J00000000000000000000011','01J00000000000000000000001',1,'2026-01-01T00:01:00Z','2026-01-01T00:02:00Z','succeeded','token=REDACTED','', '{"rows":1}', NULL),
('01J00000000000000000000012','01J00000000000000000000001',2,'2026-01-01T00:03:00Z',NULL,'failed','out','secret stderr','{}','safe error')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO ops.attempts (id, execution_id, number, started_at, status, stdout, stderr) VALUES ('attempt-relay', '01J00000000000000000000004', 1, '2026-01-04T00:01:00Z', 'failed', 'relay output', 'relay error')`); err != nil {
		t.Fatal(err)
	}
	for n := 3; n <= 103; n++ {
		if _, err := owner.Exec(ctx, `INSERT INTO ops.attempts (id, execution_id, number, started_at, status, stdout, stderr) VALUES ($1, $2, $3, '2026-01-03T00:00:00Z', 'succeeded', $4, '')`, fmt.Sprintf("attempt-%03d", n), "01J00000000000000000000001", n, fmt.Sprintf("line-%03d", n)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `INSERT INTO ops.attempts (id, execution_id, number, started_at, status, stdout, stderr) VALUES ('attempt-200', '01J00000000000000000000002', 1, '2026-01-04T00:00:00Z', 'running', 'token=REDACTED', 'secret stderr')`); err != nil {
		t.Fatal(err)
	}

	if _, err := owner.Exec(ctx, `INSERT INTO ops.attempts (id, execution_id, number, started_at, finished_at, status, stdout, stderr) VALUES ('attempt-model', '01J00000000000000000000003', 1, '2026-01-05T00:00:00Z', '2026-01-05T00:01:00Z', 'succeeded', 'model output', '')`); err != nil {
		t.Fatal(err)
	}

	readCfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	readCfg.ConnConfig.Database = db
	readCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, readCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := owner.Exec(ctx, `INSERT INTO ops.job_overrides(job_ref,paused) VALUES('job/first',true)`); err != nil {
		t.Fatal(err)
	}
	declared := &config.Config{Jobs: map[string]config.Job{"first": {Purpose: "Existing job"}, "new": {Purpose: "No runs yet"}}, Models: map[string]config.Model{"mart.current": {Schedule: "0 * * * *"}}}
	listed, err := ListJobs(ctx, pool, declared)
	if err != nil || len(listed) != 8 || listed[0].Ref != jobs.BackupRef || listed[1].Ref != jobs.CleanupRef || listed[2].Ref != "ddp:comms_relay" || listed[3].Ref != jobs.HealthRef || listed[4].Ref != "job/first" || listed[5].Ref != "job/new" || listed[6].Ref != "model/mart.current" || listed[7].Ref != "model/mart.first" {
		t.Fatalf("readonly job list: %+v %v", listed, err)
	}
	if listed[2].Action != "notify" || listed[2].Schedule != "" || len(listed[2].Tags) != 1 || listed[2].Tags[0] != "system" || listed[2].NewestRun == nil {
		t.Fatalf("historical relay declaration: %+v", listed[2])
	}
	if listed[3].Action != "check" || listed[3].Schedule != "* * * * *" {
		t.Fatalf("health declaration: %+v", listed[3])
	}
	job, err := ShowJob(ctx, pool, declared, "job/first")
	if err != nil || !job.Paused || job.NewestRun == nil || job.NewestRun.ID != "01J00000000000000000000002" {
		t.Fatalf("readonly job detail: %+v %v", job, err)
	}
	scheduled, err := ShowJob(ctx, pool, declared, "model/mart.current")
	if err != nil || scheduled.Schedule != "0 * * * *" || scheduled.NewestRun != nil {
		t.Fatalf("unrun scheduled model: %+v %v", scheduled, err)
	}
	historical, err := ShowJob(ctx, pool, declared, "model/mart.first")
	if err != nil || historical.NewestRun == nil || historical.NewestRun.Status != "succeeded" {
		t.Fatalf("removed model history: %+v %v", historical, err)
	}
	relay, err := ShowJob(ctx, pool, declared, jobs.CommsRelayRef)
	if err != nil || relay.NewestRun == nil || relay.NewestRun.Status != "failed" || relay.Schedule != "" {
		t.Fatalf("relay history without SMTP: %+v %v", relay, err)
	}
	relayRuns, err := ListRuns(ctx, pool, jobs.CommsRelayRef, 1)
	if err != nil || len(relayRuns) != 1 || relayRuns[0].ID != "01J00000000000000000000004" {
		t.Fatalf("relay runs: %#v %v", relayRuns, err)
	}
	relayDetail, err := Inspect(ctx, pool, declared, jobs.CommsRelayRef)
	if err != nil || relayDetail.Type != "job" || relayDetail.Job == nil || relayDetail.Job.NewestRun == nil || relayDetail.Job.NewestRun.ID != "01J00000000000000000000004" {
		t.Fatalf("relay inspection: %+v %v", relayDetail, err)
	}
	if _, err := ShowJob(ctx, pool, declared, "job/missing"); err == nil {
		t.Fatal("unknown job accepted")
	}
	filtered, err := ListRuns(ctx, pool, "model/mart.first", 10)
	if err != nil || len(filtered) != 1 || filtered[0].ID != "01J00000000000000000000003" {
		t.Fatalf("model run filter: %+v %v", filtered, err)
	}
	runs, err := ListRuns(ctx, pool, "job/first", 1)
	if err != nil || len(runs) != 1 || runs[0].ID != "01J00000000000000000000002" || runs[0].Status != "queued" || runs[0].MaxAttempts != 1 {
		t.Fatalf("unexpected runs: %#v, %v", runs, err)
	}
	detail, err := ShowRun(ctx, pool, "execution/01J00000000000000000000001")
	if err != nil || detail.TotalAttempts != 103 || len(detail.Attempts) != 100 || detail.Attempts[0].ID != "attempt-103" {
		t.Fatalf("unexpected detail: %#v, %v", detail, err)
	}
	logs, err := Logs(ctx, pool, "job/first", 1)
	if err != nil || len(logs) != 1 || logs[0].ID != "attempt-200" || logs[0].Stdout != "token=REDACTED" || logs[0].Stderr != "secret stderr" {
		t.Fatalf("unexpected logs: %#v, %v", logs, err)
	}
	logs, err = Logs(ctx, pool, "execution/01J00000000000000000000001", 1)
	if err != nil || len(logs) != 1 || logs[0].ID != "attempt-103" {
		t.Fatalf("unexpected execution logs: %#v, %v", logs, err)
	}
	logs, err = Logs(ctx, pool, "model/mart.first", 1)
	if err != nil || len(logs) != 1 || logs[0].ID != "attempt-model" {
		t.Fatalf("unexpected scheduled model logs: %#v, %v", logs, err)
	}
	logs, err = Logs(ctx, pool, jobs.CommsRelayRef, 1)
	if err != nil || len(logs) != 1 || logs[0].ID != "attempt-relay" {
		t.Fatalf("relay logs: %#v %v", logs, err)
	}
	if _, err := ListRuns(ctx, pool, "job/does-not-exist", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := ShowRun(ctx, pool, "execution/not-a-ulid"); err == nil {
		t.Fatal("invalid execution reference accepted")
	}
	if _, err := Logs(ctx, pool, "bad", 1); err == nil {
		t.Fatal("invalid log reference accepted")
	}
	if _, err := Logs(ctx, pool, "model/", 1); err == nil {
		t.Fatal("invalid model log reference accepted")
	}
	if _, err := ListRuns(ctx, pool, "", 101); err == nil {
		t.Fatal("out-of-range limit accepted")
	}
}

func TestInspectionRefAcceptsOnlyKnownSystemRef(t *testing.T) {
	for _, ref := range []string{jobs.BackupRef, jobs.CommsRelayRef, jobs.HealthRef, jobs.CleanupRef, "job/example", "model/mart.example"} {
		if err := validateInspectionRef(ref); err != nil {
			t.Errorf("validateInspectionRef(%q): %v", ref, err)
		}
	}
	for _, ref := range []string{"ddp:other", "ddp:comms_relay/extra"} {
		if err := validateInspectionRef(ref); err == nil {
			t.Errorf("validateInspectionRef(%q) accepted arbitrary system ref", ref)
		}
	}
	withSMTP := &config.Config{Comms: config.Comms{SMTP: &config.SMTP{Addr: "smtp.example.test:587", From: "sender@example.test", TLS: "none"}}}
	if got := definitions(withSMTP)[jobs.CommsRelayRef].Schedule; got != "* * * * *" {
		t.Fatalf("relay schedule with SMTP: %q", got)
	}
}
