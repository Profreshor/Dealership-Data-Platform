package inspect

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func diagnosticDB(t *testing.T) (*pgxpool.Pool, *config.Config) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_diagnose_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
		_ = admin.Close(context.Background())
	})
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	c, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), c.Conn()); err != nil {
		t.Fatal(err)
	}
	c.Release()
	cfg, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return pool, cfg
}

func TestDiagnosisRecordedEvidenceAndRecovery(t *testing.T) {
	pool, _ := diagnosticDB(t)
	cfg, err := config.Load("../testdata/reporting/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	readCfg := pool.Config().Copy()
	readCfg.MaxConns = 1
	readCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	reader, err := pgxpool.NewWithConfig(ctx, readCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	seed := func(ref, state string, age int) string {
		t.Helper()
		id := ulid.Make().String()
		_, err := pool.Exec(ctx, `INSERT INTO ops.executions(id,job_ref,status,scheduled_at,finished_at) VALUES($1,$2,$3,clock_timestamp()-$4*interval '1 minute',clock_timestamp())`, id, ref, state, age)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	failed := seed("job/sync_customers", "failed", 5)
	seed("job/refresh_customers", "succeeded", 4)
	seed("job/unrelated", "failed", 1)
	_, err = pool.Exec(ctx, `INSERT INTO ops.attempts(id,execution_id,number,status,stdout,stderr,error) VALUES($1,$2,2,'failed',repeat('x',5000)||'tail',repeat('y',5000),repeat('z',3000))`, ulid.Make().String(), failed)
	if err != nil {
		t.Fatal(err)
	}
	get := func(ref string) Diagnosis {
		t.Helper()
		d, err := Diagnose(ctx, reader, cfg, ref)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, ref := range []string{"job/sync_customers", "table/synthetic.customers", "page/customers", "integration/synthetic"} {
		d := get(ref)
		if d.State != "failing" || len(d.Failures) != 1 || d.Failures[0].ID != failed || len(d.IntegrationRefs) != 1 || d.IntegrationRefs[0] != "integration/synthetic" || len(d.AffectedOutputs) != 1 || d.AffectedOutputs[0] != "table/synthetic.customers" {
			t.Fatalf("%s: %+v", ref, d)
		}
		a := d.Failures[0].LastAttempt
		if a == nil || a.Number != 2 || len(a.Stdout) != 4096 || !strings.HasSuffix(a.Stdout, "tail") || len(a.Stderr) != 4096 || a.Error == nil || len(*a.Error) != 2048 {
			t.Fatalf("unbounded or missing last attempt: %+v", a)
		}
	}
	seed("job/sync_customers", "succeeded", 2)
	if d := get("page/customers"); d.State != "ok" || len(d.Failures) != 0 {
		t.Fatalf("recovered: %+v", d)
	}
	if d := get("execution/" + failed); d.Scope != "execution" || d.State != "failing" || d.Execution == nil || d.Execution.ID != failed {
		t.Fatalf("historical: %+v", d)
	}
	cfg.Comms.Groups = map[string]config.Group{"ops": {Recipients: []string{"ops@example.test"}}}
	cfg.Health["fresh"] = config.Health{Kind: "freshness", Target: "table/synthetic.customers", Column: "_loaded_at", MaxAge: "1h", Severity: "warning", Notify: []string{"group/ops"}}
	if d := get("page/customers"); d.State != "unknown" || len(d.Findings) != 1 || d.Findings[0].Code != "health_not_observed" {
		t.Fatalf("missing health: %+v", d)
	}
	observation := `{"ref":"health/fresh","target":"table/synthetic.customers","state":"ok","severity":"warning","message":"fresh"}`
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_evaluations(id,rule_ref,observed_at,observation,state,severity) VALUES('health-eval','health/fresh',clock_timestamp(),$1,'ok','warning')`, observation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO ops.alert_state(rule_ref,state,evaluation_id) VALUES('health/fresh','ok','health-eval')`)
	if err != nil {
		t.Fatal(err)
	}
	if d := get("page/customers"); d.State != "ok" {
		t.Fatalf("observed health: %+v", d)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_evaluations SET observed_at=clock_timestamp()-interval '4 minutes'`)
	if err != nil {
		t.Fatal(err)
	}
	if d := get("health/fresh"); d.State != "unknown" || d.Findings[0].Code != "health_stale" {
		t.Fatalf("stale health: %+v", d)
	}
	delete(cfg.Health, "fresh")
	delete(cfg.Jobs, "refresh_customers")
	delete(cfg.Jobs, "sync_customers")
	if d := get("job/sync_customers"); d.Declared || d.State != "ok" || len(d.Upstream) != 0 {
		t.Fatalf("removed job: %+v", d)
	}
	for _, ref := range []string{"job/missing", "execution/not-an-id", "execution/" + ulid.Make().String(), "x' OR true--"} {
		if _, err := Diagnose(ctx, reader, cfg, ref); err == nil {
			t.Fatalf("accepted missing ref %q", ref)
		}
	}
	if _, err := reader.Exec(ctx, `UPDATE ops.executions SET status='failed'`); err == nil {
		t.Fatal("read-only role unexpectedly wrote")
	}
}
