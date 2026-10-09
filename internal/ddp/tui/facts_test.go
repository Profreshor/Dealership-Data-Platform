package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/inspect"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestSharedFactsAgainstPostgres(t *testing.T) {
	owner, reader, cfg := tuiTestDB(t)
	ctx := t.Context()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repoRoot(t), "ddp"), filepath.Join(root, "ddp")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jobs", "tui_probe.py"), []byte("def run(ctx):\n    print('true stdout')\n    print('true stderr', file=__import__('sys').stderr)\n    raise RuntimeError('true failure')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Jobs["tui_probe"] = config.Job{Purpose: "TUI evidence", Action: "check", Python: "jobs.tui_probe"}
	jobRef := "job/tui_probe"
	childURL := owner.Config().ConnConfig.Copy()
	t.Setenv("JOB_DATABASE_URL", fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable", childURL.Host, childURL.Port, childURL.User, childURL.Password, childURL.Database))
	failed, err := jobs.Run(ctx, owner, cfg, root, jobRef)
	if err == nil || failed.Status != "failed" {
		t.Fatalf("seed failed execution: %+v %v", failed, err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO ops.attempts(id,execution_id,number,started_at,finished_at,status,stdout,stderr,payload_expired_at)
VALUES($1,$2,99,clock_timestamp()-interval '1 hour',clock_timestamp()-interval '1 hour','failed','expired stdout','expired stderr',clock_timestamp()-interval '1 hour')`, ulid.Make().String(), failed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO ops.health_evaluations(id,rule_ref,observed_at,observation,state,severity)
VALUES('tui-health-eval','health/tui','2026-09-04T12:00:00Z','{"ref":"health/tui","state":"failing","severity":"warning","message":"persisted failure","notify":[]}','failing','warning');
INSERT INTO ops.alert_state(rule_ref,state,evaluation_id) VALUES('health/tui','failing','tui-health-eval')`); err != nil {
		t.Fatal(err)
	}

	before := tableCounts(t, owner)
	wantJobs, err := inspect.ListJobs(ctx, reader, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantRuns, err := inspect.ListRuns(ctx, reader, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	wantOverview, err := inspect.Overview(ctx, reader, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantHealth, err := health.Latest(ctx, reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, screen := range []Screen{Overview, Failures, Jobs, Runs, Logs, Integrations, Models, Tables, Health, Communications} {
		got, err := Load(ctx, reader, cfg, screen)
		if err != nil {
			t.Fatalf("Load(%s): %v", screen, err)
		}
		compareRows(t, screen, got.Rows, wantJobs, wantRuns, wantOverview.RecentFailures, wantHealth)
	}
	if after := tableCounts(t, owner); !reflect.DeepEqual(before, after) {
		t.Fatalf("Load changed evidence tables: before=%v after=%v", before, after)
	}

	actualInspect, err := inspect.Inspect(ctx, reader, cfg, "execution/"+failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	actualDiagnose, err := inspect.Diagnose(ctx, reader, cfg, "execution/"+failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode string
		want any
	}{
		{"inspect", actualInspect},
		{"diagnose", actualDiagnose},
	} {
		got, err := Detail(ctx, reader, cfg, "execution/"+failed.ID, tc.mode)
		if err != nil {
			t.Fatalf("Detail(%s): %v", tc.mode, err)
		}
		assertJSONEqual(t, got, tc.want)
	}
	logs, err := Detail(ctx, reader, cfg, "execution/"+failed.ID, "logs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "true stdout") || !strings.Contains(logs, "true stderr") || !strings.Contains(logs, "expired") {
		t.Fatalf("log detail lost evidence: %q", logs)
	}
	if strings.Contains(logs, "expired stdout") || strings.Contains(logs, "expired stderr") {
		t.Fatalf("expired payload leaked: %q", logs)
	}
	if after := tableCounts(t, owner); !reflect.DeepEqual(before, after) {
		t.Fatalf("Detail changed evidence tables: before=%v after=%v", before, after)
	}

	ctxCancel, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Load(ctxCancel, reader, cfg, Overview); err == nil {
		t.Fatal("cancelled Load returned nil error")
	}
	if _, err := Detail(ctxCancel, reader, cfg, "execution/"+failed.ID, "inspect"); err == nil {
		t.Fatal("cancelled Detail returned nil error")
	}
	reader.Close()
	if got, err := Load(ctx, reader, cfg, Overview); err == nil || len(got.Rows) != 0 {
		t.Fatalf("database failure returned facts: %+v %v", got, err)
	}
}

func compareRows(t *testing.T, screen Screen, rows []Row, jobs []inspect.Job, runs, failures []inspect.Run, healthRows []health.Evaluation) {
	t.Helper()
	want := map[string]Row{}
	switch screen {
	case Jobs:
		for _, job := range jobs {
			row := Row{Ref: job.Ref, JobRef: job.Ref, State: "declared"}
			if job.Paused {
				row.State = "paused"
			} else if job.NewestRun != nil {
				row.State = job.NewestRun.Status
			}
			want[row.Ref] = row
		}
	case Runs:
		for _, run := range runs {
			want["execution/"+run.ID] = Row{Ref: "execution/" + run.ID, State: run.Status, JobRef: run.JobRef}
		}
	case Failures:
		for _, run := range failures {
			want["execution/"+run.ID] = Row{Ref: "execution/" + run.ID, State: run.Status, JobRef: run.JobRef}
		}
	case Health:
		for _, evaluation := range healthRows {
			want[evaluation.Ref] = Row{Ref: evaluation.Ref, State: evaluation.State}
		}
	}
	if len(want) == 0 {
		return
	}
	if len(rows) != len(want) {
		t.Fatalf("%s row count=%d want=%d", screen, len(rows), len(want))
	}
	got := map[string]Row{}
	for _, row := range rows {
		got[row.Ref] = row
	}
	for ref, expected := range want {
		actual, ok := got[ref]
		if !ok || actual.State != expected.State || actual.JobRef != expected.JobRef {
			t.Fatalf("%s row %q=%+v, want state/job=%q/%q; rows=%+v", screen, ref, actual, expected.State, expected.JobRef, rows)
		}
	}
}

func assertJSONEqual(t *testing.T, got string, want any) {
	t.Helper()
	var actual, expected any
	if err := json.Unmarshal([]byte(got), &actual); err != nil {
		t.Fatalf("invalid detail JSON: %v (%q)", err, got)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &expected); err != nil {
		t.Fatal(err)
	}
	if gotMap, ok := actual.(map[string]any); ok {
		delete(gotMap, "observed_at")
	}
	if wantMap, ok := expected.(map[string]any); ok {
		delete(wantMap, "observed_at")
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("detail mismatch: got=%s want=%s", got, b)
	}
}

func tuiTestDB(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, *config.Config) {
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
	name := "ddp_tui_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(cleanup)
	})
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = name
	owner, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	conn, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	readerCfg := pc.Copy()
	readerCfg.MaxConns = 1
	readerCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	reader, err := pgxpool.NewWithConfig(ctx, readerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	cfg, err := config.Load(filepath.Join(repoRoot(t), "internal/ddp/testdata/reporting/ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return owner, reader, cfg
}

func tableCounts(t *testing.T, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for _, table := range []string{"ops.executions", "ops.attempts", "ddp.audit", "ops.outbox"} {
		var n int64
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	return counts
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "..")
}
