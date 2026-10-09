package health

import (
	"context"
	"slices"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestDeploymentAlertsAndRecovery(t *testing.T) {
	pool := healthDB(t)
	ctx := t.Context()
	cfg := &config.Config{Comms: config.Comms{SMTP: &config.SMTP{Addr: "localhost:2525", From: "sender@example.test", TLS: "none"}, Groups: map[string]config.Group{"platform_ops": {Recipients: []string{"ops@example.test"}}}}}
	insert := func(id, status, phase string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO ops.deployments(id,image,previous_image,revision,status,phase) VALUES($1,'sha256:image','sha256:previous','revision',$2,$3)`, id, status, phase); err != nil {
			t.Fatal(err)
		}
	}
	if got := platform.Deployment(ctx, pool); got != nil {
		t.Fatalf("empty deployment history: %+v", got)
	}
	if slices.ContainsFunc(Platform(ctx, pool, t.TempDir(), cfg), func(o Observation) bool { return o.Ref == "ddp:deployment" }) {
		t.Fatal("empty deployment history became active")
	}
	failed := ulid.Make().String()
	insert(failed, "failed", "startup")
	for range 2 {
		if _, err := Evaluate(ctx, pool, cfg, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	var alerts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.alerts WHERE rule_ref='ddp:deployment' AND kind='alert'`).Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("deployment alerts=%d err=%v", alerts, err)
	}
	var outbox int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.outbox WHERE effect_key IN (SELECT 'health/' || id FROM ops.alerts WHERE rule_ref='ddp:deployment')`).Scan(&outbox); err != nil || outbox != 1 {
		t.Fatalf("deployment outbox=%d err=%v", outbox, err)
	}
	succeeded := ulid.Make().String()
	insert(succeeded, "succeeded", "ready")
	if _, err := pool.Exec(ctx, "UPDATE ops.deployments SET finished_at='2026-01-01T00:00:00Z' WHERE id=ANY($1)", []string{failed, succeeded}); err != nil {
		t.Fatal(err)
	}
	if succeeded <= failed {
		t.Fatal("test IDs do not order success after failure")
	}
	observed, err := Evaluate(ctx, pool, cfg, t.TempDir())
	if err != nil || !slices.ContainsFunc(observed, func(o Evaluation) bool { return o.Ref == "ddp:deployment" && o.State == "ok" }) {
		t.Fatalf("successful deployment observed=%+v err=%v", observed, err)
	}
	if _, err := Evaluate(ctx, pool, cfg, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.alerts WHERE rule_ref='ddp:deployment' AND kind='recovery'`).Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("deployment recoveries=%d err=%v", alerts, err)
	}
	readCfg := pool.Config().Copy()
	readCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	reader, err := pgxpool.NewWithConfig(ctx, readCfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := platform.Deployment(ctx, reader); got == nil || got.State != "ok" {
		t.Fatalf("readonly deployment: %+v", got)
	}
	reader.Close()
	if got := platform.Deployment(ctx, reader); got == nil || got.State != "unknown" {
		t.Fatalf("closed deployment: %+v", got)
	}
}
