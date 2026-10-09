package metrics

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func databaseTestPool(t *testing.T, migrated, readOnly bool) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}
	name := fmt.Sprintf("ddp_metrics_%d", time.Now().UnixNano())
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable database %s: %v", name, err)
		}
		_ = admin.Close(ctx)
	})
	newPool := func(role string) *pgxpool.Pool {
		pc, err := pgxpool.ParseConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		pc.ConnConfig.Database = name
		if role != "" {
			pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
				_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
				return err
			}
		}
		pool, err := pgxpool.NewWithConfig(t.Context(), pc)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	owner := newPool("")
	if migrated {
		conn, err := owner.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		err = migrate.Up(t.Context(), conn.Conn())
		conn.Release()
		if err != nil {
			t.Fatal(err)
		}
	}
	if readOnly {
		return owner, newPool("ddp_readonly")
	}
	return owner, owner
}

func gatherDatabase(t *testing.T, pool *pgxpool.Pool, cfg *config.Config) map[string]*dto.MetricFamily {
	t.Helper()
	r := prometheus.NewPedanticRegistry()
	if err := r.Register(NewDatabase(pool, cfg)); err != nil {
		t.Fatal(err)
	}
	families, err := r.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*dto.MetricFamily, len(families))
	for _, family := range families {
		out[family.GetName()] = family
	}
	return out
}

func metricLabels(m *dto.Metric) string {
	labels := make([]string, 0, len(m.Label))
	for _, label := range m.Label {
		labels = append(labels, label.GetName()+"="+label.GetValue())
	}
	slices.Sort(labels)
	return strings.Join(labels, ",")
}

func metricByLabels(t *testing.T, family *dto.MetricFamily, labels string) *dto.Metric {
	t.Helper()
	if family != nil {
		for _, metric := range family.Metric {
			if metricLabels(metric) == labels {
				return metric
			}
		}
	}
	t.Fatalf("missing metric %s in %+v", labels, family)
	return nil
}

func assertOnlyDown(t *testing.T, families map[string]*dto.MetricFamily) {
	t.Helper()
	up := families["ddp_metrics_database_up"]
	if len(families) != 1 || up == nil || len(up.Metric) != 1 || up.Metric[0].GetGauge().GetValue() != 0 {
		t.Fatalf("failure published partial database metrics: %+v", families)
	}
}

func TestDatabaseHealthChecksFollowActiveEvaluators(t *testing.T) {
	cfg := &config.Config{}
	collector := NewDatabase(nil, cfg).(*databaseCollector)
	if slices.Contains(collector.checks(false), "ddp:job/ddp:comms_relay") {
		t.Fatal("disabled comms relay exported a health check")
	}
	for _, ref := range []string{"ddp:backup", "ddp:job/ddp:backup"} {
		if slices.Contains(collector.checks(false), ref) {
			t.Fatal("disabled backup exported a health check", ref)
		}
	}
	cfg.Comms.SMTP = &config.SMTP{}
	if !slices.Contains(collector.checks(false), "ddp:job/ddp:comms_relay") {
		t.Fatal("enabled comms relay omitted its health check")
	}
	cfg.Deploy.Backup = &config.Backup{}
	for _, ref := range []string{"ddp:backup", "ddp:job/ddp:backup"} {
		if !slices.Contains(collector.checks(false), ref) {
			t.Fatal("enabled backup omitted its health check", ref)
		}
	}
}

func TestDatabaseSnapshotAgainstPostgres(t *testing.T) {
	owner, reader := databaseTestPool(t, true, true)
	cfg := &config.Config{
		Jobs:   map[string]config.Job{"alpha": {}},
		Models: map[string]config.Model{"mart.rollup": {Schedule: "0 * * * *"}},
		Health: map[string]config.Health{"custom": {}},
	}
	_, err := owner.Exec(t.Context(), `
INSERT INTO ops.executions(id,job_ref,scheduled_at,started_at,finished_at,status,dispatch,available_at) VALUES
('alpha-ok','job/alpha',now(),now()-interval '10 seconds',now(),'succeeded','manual',now()),
('alpha-failed','job/alpha',now(),now(),now()-interval '1 second','failed','manual',now()),
('alpha-interrupted','job/alpha',now(),now(),NULL,'interrupted','manual',now()),
('alpha-skipped','job/alpha',now(),now()-interval '3 seconds',now(),'skipped','manual',now()),
('model-ok','model/mart.rollup',now(),now()-interval '5 seconds',now(),'succeeded','manual',now()),
('oldest-due','job/alpha',now(),NULL,NULL,'queued','scheduler',clock_timestamp()-interval '17 seconds'),
('future','job/alpha',now(),NULL,NULL,'queued','scheduler',clock_timestamp()+interval '1 hour'),
('manual-due','job/alpha',now(),NULL,NULL,'queued','manual',clock_timestamp()-interval '1 hour');
INSERT INTO ops.executions(id,job_ref,scheduled_at,started_at,finished_at,status,dispatch,available_at)
SELECT 'removed-'||n,'job/removed-'||n,now(),now()-interval '2 seconds',now(),'succeeded','manual',now()
FROM generate_series(1,1001) AS n;
INSERT INTO ops.health_evaluations(id,rule_ref,observed_at,observation,state,severity) VALUES
('database-old','ddp:database','2026-01-01T00:00:00Z','{}','failing','critical'),
('database-current','ddp:database','2026-01-02T00:00:00Z','{}','ok','warning'),
('database-newer-unselected','ddp:database','2026-01-03T00:00:00Z','{}','failing','critical'),
('alpha-health','ddp:job/job/alpha','2026-01-04T00:00:00Z','{}','ok','warning'),
('custom-health','health/custom','2026-01-05T00:00:00Z','{}','unknown','warning'),
('removed-health','health/removed','2026-01-06T00:00:00Z','{}','failing','critical');
INSERT INTO ops.alert_state(rule_ref,state,evaluation_id) VALUES
('ddp:database','ok','database-current'),
('ddp:job/job/alpha','ok','alpha-health'),
('health/custom','unknown','custom-health'),
('health/removed','failing','removed-health');
INSERT INTO ops.outbox(id,effect_key,payload_hash,template,context,sender,recipients,status)
SELECT status||'-'||n,status||'-'||n,decode(repeat('00',32),'hex'),'alert','{}','sender@example.test',ARRAY['recipient@example.test'],status
FROM (VALUES ('pending',1),('delivering',2),('delivered',3),('failed',4)) AS wanted(status,total)
CROSS JOIN LATERAL generate_series(1,total) AS n`)
	if err != nil {
		t.Fatal(err)
	}
	var auditBefore int
	if err := owner.QueryRow(t.Context(), "SELECT count(*) FROM ddp.audit").Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	families := gatherDatabase(t, reader, cfg)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("snapshot exceeded bound: %v", elapsed)
	}
	if got := families["ddp_metrics_database_up"].Metric[0].GetGauge().GetValue(); got != 1 {
		t.Fatalf("database_up=%v", got)
	}
	executions := families["ddp_job_executions_total"]
	wantCounts := map[string]float64{
		"job=job/alpha,status=succeeded":         1,
		"job=job/alpha,status=failed":            1,
		"job=job/alpha,status=interrupted":       1,
		"job=job/alpha,status=skipped":           1,
		"job=model/mart.rollup,status=succeeded": 1,
		"job=undeclared,status=succeeded":        1001,
	}
	if len(executions.Metric) != len(wantCounts) {
		t.Fatalf("execution series=%d want %d", len(executions.Metric), len(wantCounts))
	}
	for labels, want := range wantCounts {
		if got := metricByLabels(t, executions, labels).GetCounter().GetValue(); got != want {
			t.Errorf("executions{%s}=%v want %v", labels, got, want)
		}
	}
	durations := families["ddp_job_duration_seconds"]
	wantDurations := map[string]struct {
		count uint64
		sum   float64
	}{
		"job=job/alpha,status=succeeded":         {1, 10},
		"job=job/alpha,status=skipped":           {1, 3},
		"job=model/mart.rollup,status=succeeded": {1, 5},
		"job=undeclared,status=succeeded":        {1001, 2002},
	}
	if len(durations.Metric) != len(wantDurations) {
		t.Fatalf("duration series=%d want %d", len(durations.Metric), len(wantDurations))
	}
	for labels, want := range wantDurations {
		summary := metricByLabels(t, durations, labels).GetSummary()
		if summary.GetSampleCount() != want.count || summary.GetSampleSum() != want.sum {
			t.Errorf("duration{%s}=(%d,%v) want (%d,%v)", labels, summary.GetSampleCount(), summary.GetSampleSum(), want.count, want.sum)
		}
	}
	health := families["ddp_health_state"]
	checks := []string{"health/custom", "ddp:database", "ddp:disk", "ddp:job/ddp:cleanup", "ddp:job/job/alpha", "ddp:job/model/mart.rollup", "ddp:outbox", "ddp:scheduler"}
	if len(health.Metric) != len(checks)*3 {
		t.Fatalf("health series=%d want %d", len(health.Metric), len(checks)*3)
	}
	states := map[string]string{"ddp:database": "ok", "ddp:job/job/alpha": "ok"}
	for _, check := range checks {
		want := states[check]
		if want == "" {
			want = "unknown"
		}
		for _, state := range []string{"ok", "failing", "unknown"} {
			got := metricByLabels(t, health, "check="+check+",state="+state).GetGauge().GetValue()
			if got != map[bool]float64{true: 1}[state == want] {
				t.Errorf("health{%s,%s}=%v want state %s", check, state, got, want)
			}
		}
	}
	observed := families["ddp_health_observed_timestamp_seconds"]
	if len(observed.Metric) != len(checks) {
		t.Fatalf("observed series=%d want %d", len(observed.Metric), len(checks))
	}
	wantObserved, _ := time.Parse(time.RFC3339, "2026-01-02T00:00:00Z")
	if got := metricByLabels(t, observed, "check=ddp:database").GetGauge().GetValue(); got != float64(wantObserved.Unix()) {
		t.Errorf("database observed timestamp=%v", got)
	}
	if got := metricByLabels(t, observed, "check=ddp:disk").GetGauge().GetValue(); got != 0 {
		t.Errorf("missing check observed timestamp=%v", got)
	}
	outbox := families["ddp_outbox_depth"]
	for i, status := range []string{"pending", "delivering", "delivered", "failed"} {
		if got := metricByLabels(t, outbox, "status="+status).GetGauge().GetValue(); got != float64(i+1) {
			t.Errorf("outbox{%s}=%v want %d", status, got, i+1)
		}
	}
	lag := families["ddp_scheduler_lag_seconds"].Metric[0].GetGauge().GetValue()
	if lag < 17 || lag > 20 {
		t.Errorf("scheduler lag=%v, expected age of oldest due scheduler row", lag)
	}
	if _, err := owner.Exec(t.Context(), "DELETE FROM ops.executions WHERE id='oldest-due'"); err != nil {
		t.Fatal(err)
	}
	if got := gatherDatabase(t, reader, cfg)["ddp_scheduler_lag_seconds"].Metric[0].GetGauge().GetValue(); got != 0 {
		t.Errorf("scheduler lag with only future and manual rows=%v", got)
	}
	var auditAfter int
	if err := owner.QueryRow(t.Context(), "SELECT count(*) FROM ddp.audit").Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Errorf("metrics collection mutated audit: before=%d after=%d err=%v", auditBefore, auditAfter, err)
	}
}

func TestDeploymentMetricsActivateAfterHistoryAndEvaluation(t *testing.T) {
	owner, reader := databaseTestPool(t, true, true)
	cfg := &config.Config{}
	if families := gatherDatabase(t, reader, cfg); slices.ContainsFunc(families["ddp_health_state"].Metric, func(m *dto.Metric) bool {
		return metricLabels(m) == "check=ddp:deployment,state=unknown"
	}) {
		t.Fatal("deployment metric exported without history")
	}
	if _, err := owner.Exec(t.Context(), `INSERT INTO ops.deployments(id,image,previous_image,revision,status,phase) VALUES('metric-failed','sha256:image','sha256:previous','revision','failed','startup')`); err != nil {
		t.Fatal(err)
	}
	if got := metricByLabels(t, gatherDatabase(t, reader, cfg)["ddp_health_state"], "check=ddp:deployment,state=unknown").GetGauge().GetValue(); got != 1 {
		t.Fatalf("deployment pre-evaluation state=%v", got)
	}
	before, err := health.Evaluate(t.Context(), owner, cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(before, func(e health.Evaluation) bool { return e.Ref == "ddp:deployment" && e.State == "failing" }) {
		t.Fatalf("deployment evaluation missing: %+v", before)
	}
	families := gatherDatabase(t, reader, cfg)
	deployment := families["ddp_health_state"]
	if got := metricByLabels(t, deployment, "check=ddp:deployment,state=failing").GetGauge().GetValue(); got != 1 {
		t.Fatalf("deployment failing state=%v", got)
	}
	if got := metricByLabels(t, families["ddp_health_observed_timestamp_seconds"], "check=ddp:deployment").GetGauge().GetValue(); got == 0 {
		t.Fatal("deployment observation timestamp missing")
	}
}

type closeAfterLagQuery struct{}

type closeAfterLagKey struct{}

func (closeAfterLagQuery) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, closeAfterLagKey{}, strings.Contains(data.SQL, "clock_timestamp()-available_at"))
}

func (closeAfterLagQuery) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && ctx.Value(closeAfterLagKey{}) == true {
		_ = conn.PgConn().Close(ctx)
	}
}

func TestDatabaseCommitFailurePublishesOnlyDown(t *testing.T) {
	owner, _ := databaseTestPool(t, true, false)
	pc, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = owner.Config().ConnConfig.Database
	pc.ConnConfig.Tracer = closeAfterLagQuery{}
	pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	reader, err := pgxpool.NewWithConfig(t.Context(), pc)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	assertOnlyDown(t, gatherDatabase(t, reader, &config.Config{}))
}

func TestDatabaseFailuresPublishOnlyDown(t *testing.T) {
	t.Run("nil pool", func(t *testing.T) {
		assertOnlyDown(t, gatherDatabase(t, nil, nil))
	})
	t.Run("missing tables", func(t *testing.T) {
		_, pool := databaseTestPool(t, false, false)
		assertOnlyDown(t, gatherDatabase(t, pool, nil))
	})
	t.Run("closed pool", func(t *testing.T) {
		_, pool := databaseTestPool(t, false, false)
		pool.Close()
		assertOnlyDown(t, gatherDatabase(t, pool, nil))
	})
	t.Run("unreachable pool", func(t *testing.T) {
		pc, err := pgxpool.ParseConfig("postgres://postgres@127.0.0.1:1/ddp?sslmode=disable&connect_timeout=1")
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(t.Context(), pc)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		started := time.Now()
		assertOnlyDown(t, gatherDatabase(t, pool, nil))
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("unreachable pool exceeded bound: %v", elapsed)
		}
	})
}

func TestDatabaseBlockedQueryIsBoundedAndPublishesNoPartialSnapshot(t *testing.T) {
	owner, reader := databaseTestPool(t, true, true)
	tx, err := owner.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if _, err := tx.Exec(t.Context(), "LOCK TABLE ops.executions IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	assertOnlyDown(t, gatherDatabase(t, reader, &config.Config{}))
	elapsed := time.Since(started)
	if elapsed < 1500*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("blocked snapshot duration=%v, want statement timeout within overall bound", elapsed)
	}
}
