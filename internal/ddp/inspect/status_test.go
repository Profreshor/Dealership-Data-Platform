package inspect

import (
	"context"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthObservation(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	current := health.Evaluation{ObservedAt: now.Add(-time.Minute), Observation: health.Observation{State: "failing"}}
	for _, test := range []struct {
		name     string
		finished *time.Time
		evals    []health.Evaluation
		want     string
	}{
		{name: "missing", want: "not_observed"},
		{name: "current even when check fails", finished: timePtr(now.Add(-time.Minute)), evals: []health.Evaluation{current}, want: "current"},
		{name: "stale run", finished: timePtr(now.Add(-4 * time.Minute)), want: "stale"},
		{name: "stale evaluation", finished: timePtr(now.Add(-time.Minute)), evals: []health.Evaluation{{ObservedAt: now.Add(-4 * time.Minute)}}, want: "stale"},
		{name: "future run", finished: timePtr(now.Add(time.Second)), want: "stale"},
		{name: "future evaluation", finished: timePtr(now.Add(-time.Minute)), evals: []health.Evaluation{{ObservedAt: now.Add(time.Second)}}, want: "stale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := healthObservation(now, test.finished, test.evals, nil); got != test.want {
				t.Fatalf("health observation=%q, want %q", got, test.want)
			}
		})
	}
}

func TestHealthObservationRequiresDeclaredEvaluations(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-time.Minute)
	eval := health.Evaluation{Ref: "health/old", ObservedAt: now.Add(-time.Minute)}
	if got := healthObservation(now, &finished, []health.Evaluation{eval}, []string{"health/current"}); got != "stale" {
		t.Fatalf("health observation=%q, want stale", got)
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func TestOverviewReadOnlyPostgres(t *testing.T) {
	owner, cfg := diagnosticDB(t)
	ctx := t.Context()
	if _, err := owner.Exec(ctx, `
INSERT INTO ops.executions(id,job_ref,scheduled_at,started_at,finished_at,status)
VALUES ('health-run','ddp:health',clock_timestamp(),clock_timestamp(),clock_timestamp(),'succeeded');
INSERT INTO ops.health_evaluations(id,rule_ref,observed_at,observation,state,severity)
VALUES ('health-eval','health/example',clock_timestamp(),'{"ref":"health/example","state":"failing","severity":"warning","message":"synthetic","notify":[]}', 'failing','warning');
INSERT INTO ops.alert_state(rule_ref,state,evaluation_id) VALUES ('health/example','failing','health-eval');
INSERT INTO ops.outbox(id,effect_key,payload_hash,template,context,sender,recipients,status)
SELECT 'message-'||s, 'effect-'||s, repeat(E'\\000',32)::bytea, 'alert', '{}', 'sender@example.test', ARRAY['ops@example.test'], (ARRAY['pending','delivering','delivered','failed'])[s]
FROM generate_series(1,4) AS g(s);
INSERT INTO ops.alerts(id,rule_ref,evaluation_id,incident_id,kind,created_at,recipients,context)
VALUES ('alert-pending','health/example','health-eval','incident','alert',clock_timestamp(),ARRAY['ops@example.test'],'{}');
INSERT INTO ops.executions(id,job_ref,scheduled_at,started_at,finished_at,status)
SELECT 'failed-'||s,'job/failing',clock_timestamp()-make_interval(mins => s),clock_timestamp()-make_interval(mins => s),clock_timestamp()-make_interval(mins => s),'failed'
FROM generate_series(1,21) AS g(s)`); err != nil {
		t.Fatal(err)
	}
	readCfg := owner.Config().Copy()
	readCfg.MaxConns, readCfg.MinConns = 1, 0
	readCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	reader, err := pgxpool.NewWithConfig(ctx, readCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := Overview(ctx, reader, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.ObservedAt.IsZero() || got.HealthObservation != "current" {
		t.Fatalf("timestamps/health=%v/%q", got.ObservedAt, got.HealthObservation)
	}
	for state, want := range map[string]int64{"pending": 1, "delivering": 1, "delivered": 1, "failed": 1} {
		if got.Outbox[state] != want {
			t.Fatalf("outbox[%s]=%d, want %d", state, got.Outbox[state], want)
		}
	}
	if got.PendingAlerts != 1 || len(got.RecentFailures) != 20 || got.RecentFailures[0].Status != "failed" {
		t.Fatalf("alerts/failures=%d/%d, want 1/20", got.PendingAlerts, len(got.RecentFailures))
	}
}
