package health

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func healthDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	admin, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_health_" + strings.ToLower(ulid.Make().String())
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
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := migrate.Up(t.Context(), conn.Conn()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestAlertTransitionsAndDeferredNotification(t *testing.T) {
	pool := healthDB(t)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	now := time.Now().UTC()
	o := Observation{Ref: "health/example", Target: "table/core.example", State: "ok", Severity: "critical", Message: "Synthetic check", Notify: []string{"group/owners"}}
	save := func(state string, at time.Time) {
		t.Helper()
		o.State = state
		if _, err := record(t.Context(), conn.Conn(), o, at); err != nil {
			t.Fatal(err)
		}
	}
	save("ok", now)
	save("failing", now.Add(time.Minute))
	save("failing", now.Add(2*time.Minute))
	cfg := config.Comms{}
	if err := queueAlerts(t.Context(), conn.Conn(), cfg); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM ops.alerts WHERE message_id IS NULL AND notification_error IS NOT NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("deferred=%d %v", count, err)
	}
	cfg.SMTP = &config.SMTP{Addr: "localhost:2525", From: "sender@example.test", TLS: "none"}
	cfg.Groups = map[string]config.Group{"owners": {Recipients: []string{"owner@example.test"}}, "platform_ops": {Recipients: []string{"ops@example.test"}}}
	if err := queueAlerts(t.Context(), conn.Conn(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := queueAlerts(t.Context(), conn.Conn(), cfg); err != nil {
		t.Fatal(err)
	}
	save("failing", now.Add(24*time.Hour+time.Minute))
	save("unknown", now.Add(25*time.Hour))
	save("ok", now.Add(26*time.Hour))
	save("ok", now.Add(27*time.Hour))
	if err := queueAlerts(t.Context(), conn.Conn(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("outbox=%d %v", count, err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM ops.health_evaluations`).Scan(&count); err != nil || count != 7 {
		t.Fatalf("evaluations=%d %v", count, err)
	}
	var recipients []string
	if err := conn.QueryRow(t.Context(), `SELECT recipients FROM ops.alerts WHERE kind='alert' ORDER BY created_at DESC LIMIT 1`).Scan(&recipients); err != nil || len(recipients) != 1 || recipients[0] != "group/platform_ops" {
		t.Fatalf("technical routing=%v %v", recipients, err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT recipients FROM ops.alerts WHERE kind='recovery'`).Scan(&recipients); err != nil || len(recipients) != 2 {
		t.Fatalf("recovery routing=%v %v", recipients, err)
	}
	if _, err := conn.Exec(t.Context(), `SET ROLE ddp_readonly`); err != nil {
		t.Fatal(err)
	}
	if _, err := record(t.Context(), conn.Conn(), o, now); err == nil {
		t.Fatal("readonly recorded health")
	}
	if _, err := conn.Exec(t.Context(), `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	latest, err := Latest(t.Context(), pool)
	if err != nil || len(latest) != 1 || latest[0].State != "ok" {
		t.Fatalf("latest=%v %v", latest, err)
	}
}

func TestFinalJobFailureAndRecovery(t *testing.T) {
	for _, ref := range []string{"job/probe", "ddp:backup"} {
		t.Run(ref, func(t *testing.T) {
			pool := healthDB(t)
			cfg := &config.Config{Jobs: map[string]config.Job{"probe": {Action: "check"}}}
			if ref == "ddp:backup" {
				cfg.Deploy.Backup = &config.Backup{}
			}
			run := ulid.Make().String()
			if _, err := pool.Exec(t.Context(), `INSERT INTO ops.executions(id,job_ref,scheduled_at,status,max_attempts) VALUES($1,$2,clock_timestamp(),'queued',2)`, run, ref); err != nil {
				t.Fatal(err)
			}
			jobState := func(values []Evaluation) Evaluation {
				t.Helper()
				for _, value := range values {
					if value.Ref == "ddp:job/"+ref {
						return value
					}
				}
				t.Fatal("job observation missing")
				return Evaluation{}
			}
			observed, err := Evaluate(t.Context(), pool, cfg, t.TempDir())
			if err != nil || jobState(observed).State != "ok" {
				t.Fatalf("retrying=%v %v", observed, err)
			}
			if _, err := pool.Exec(t.Context(), `UPDATE ops.executions SET status='failed' WHERE id=$1`, run); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				observed, err = Evaluate(t.Context(), pool, cfg, t.TempDir())
				if err != nil || jobState(observed).State != "failing" || jobState(observed).ExecutionID != run {
					t.Fatalf("final=%v %v", observed, err)
				}
			}
			if _, err := pool.Exec(t.Context(), `INSERT INTO ops.executions(id,job_ref,scheduled_at,status) VALUES($1,$2,clock_timestamp(),'succeeded')`, ulid.Make().String(), ref); err != nil {
				t.Fatal(err)
			}
			if _, err := Evaluate(t.Context(), pool, cfg, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			alerts, err := Alerts(t.Context(), pool)
			alerts = slices.DeleteFunc(alerts, func(a Alert) bool { return a.Ref != "ddp:job/"+ref })
			if err != nil || len(alerts) != 2 || alerts[0].Kind != "recovery" {
				t.Fatalf("alerts=%v %v", alerts, err)
			}

		})
	}
}
