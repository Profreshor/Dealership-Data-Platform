package deploy

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

const testRepo = "ghcr.io/example/client"

func testImage(n byte) string {
	return testRepo + "@sha256:" + strings.Repeat(fmt.Sprintf("%02x", n), 32)
}

func deployDB(t *testing.T) *pgx.Conn {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	db := "ddp_deploy_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = db
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestPlanAndRecordAgainstPostgres(t *testing.T) {
	conn := deployDB(t)
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.Deploy.Image = testRepo
	current, candidate := testImage('a'), testImage('b')
	var before int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM ddp.audit").Scan(&before); err != nil {
		t.Fatal(err)
	}
	unchanged, err := Plan(ctx, conn, cfg, current, current, "", strings.Repeat("a", 40))
	if err != nil || unchanged.Action != "unchanged" || unchanged.RequiresBackup || len(unchanged.PendingMigrations) != 0 {
		t.Fatalf("unchanged plan: %#v %v", unchanged, err)
	}
	var after int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM ddp.audit").Scan(&after); err != nil || after != before {
		t.Fatalf("plan wrote database: %d -> %d (%v)", before, after, err)
	}
	rejected, err := Plan(ctx, conn, cfg, current, candidate, candidate, strings.Repeat("a", 40))
	if err != nil || rejected.Action != "rejected" {
		t.Fatalf("rejected plan: %#v %v", rejected, err)
	}
	if _, err := conn.Exec(ctx, "DELETE FROM ddp.platform_migrations WHERE id='20260905140000_deployments'"); err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(ctx, conn, cfg, current, candidate, "", strings.Repeat("a", 40)); err == nil || !strings.Contains(err.Error(), "pending migrations require") {
		t.Fatalf("missing backup accepted: %v", err)
	}
	cfg.Deploy.Backup = &config.Backup{}
	if _, err := conn.Exec(ctx, "SET default_transaction_read_only=on"); err != nil {
		t.Fatal(err)
	}
	pending, err := Plan(ctx, conn, cfg, current, candidate, "", strings.Repeat("a", 40))
	if _, err := conn.Exec(ctx, "RESET default_transaction_read_only"); err != nil {
		t.Fatal(err)
	}
	if err != nil || pending.Action != "apply" || !pending.RequiresBackup || len(pending.PendingMigrations) == 0 {
		t.Fatalf("pending plan: %#v %v", pending, err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO ddp.platform_migrations(id,checksum) VALUES('20260905140000_deployments','wrong')"); err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(ctx, conn, cfg, current, candidate, "", strings.Repeat("a", 40)); err == nil || !strings.Contains(err.Error(), "inspect migrations") {
		t.Fatalf("checksum drift accepted: %v", err)
	}
	if _, err := conn.Exec(ctx, "DELETE FROM ddp.platform_migrations WHERE id='20260905140000_deployments'"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE FUNCTION app.reject_deploy_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit rejected'; END $$;
CREATE TRIGGER reject_deploy_audit BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION app.reject_deploy_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(ctx, conn, cfg, RecordOptions{Image: candidate, PreviousImage: current, Revision: strings.Repeat("a", 40), Status: "failed", Phase: "startup"}); err == nil {
		t.Fatal("audit failure accepted")
	}
	var deployments int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM ops.deployments").Scan(&deployments); err != nil || deployments != 0 {
		t.Fatalf("record was not rolled back: %d %v", deployments, err)
	}
	if _, err := conn.Exec(ctx, "DROP TRIGGER reject_deploy_audit ON ddp.audit; DROP FUNCTION app.reject_deploy_audit()"); err != nil {
		t.Fatal(err)
	}
	// Recording remains independent of migration ledger state.
	if _, err := Record(ctx, conn, cfg, RecordOptions{Image: candidate, PreviousImage: current, Revision: strings.Repeat("a", 40), Status: "failed", Phase: "startup"}); err != nil {
		t.Fatal(err)
	}
	latest, err := Latest(ctx, conn)
	if err != nil || latest == nil || latest.Image != candidate || latest.Status != "failed" {
		t.Fatalf("latest: %#v %v", latest, err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE ddp_readonly"); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(ctx, conn, cfg, RecordOptions{Image: current, PreviousImage: candidate, Revision: strings.Repeat("b", 40), Status: "failed", Phase: "startup"}); err == nil {
		t.Fatal("readonly role forged deployment record")
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentTrustAndPrivileges(t *testing.T) {
	conn := deployDB(t)
	ctx := t.Context()
	cfg := &config.Config{Deploy: config.Deploy{Image: testRepo}}
	current, candidate, revision := testImage('a'), testImage('b'), strings.Repeat("a", 40)
	for _, input := range []struct{ current, image, failed, revision string }{
		{testRepo + ":stable", candidate, "", revision},
		{current, testRepo + ":stable", "", revision},
		{current, strings.Replace(candidate, testRepo, "ghcr.io/other/client", 1), "", revision},
		{current, candidate, "bad-reference", revision},
		{current, candidate, "", "dev"},
		{current, candidate, "", strings.Repeat("A", 40)},
	} {
		if _, err := Plan(ctx, conn, cfg, input.current, input.image, input.failed, input.revision); err == nil {
			t.Fatalf("invalid plan accepted: %+v", input)
		}
	}
	if got, err := Latest(ctx, conn); err != nil || got != nil {
		t.Fatalf("empty latest=%+v err=%v", got, err)
	}
	opts := RecordOptions{Image: candidate, PreviousImage: current, Revision: revision, Status: "failed", Phase: "startup"}
	for _, role := range []string{"ddp_api", "ddp_scheduler", "ddp_readonly"} {
		if _, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		if _, err := Record(ctx, conn, cfg, opts); err == nil {
			t.Fatalf("%s forged deployment", role)
		}
		if _, err := Latest(ctx, conn); err != nil {
			t.Fatalf("%s cannot observe: %v", role, err)
		}
		if _, err := conn.Exec(ctx, "UPDATE ops.deployments SET status='failed'"); err == nil {
			t.Fatalf("%s can alter deployment", role)
		}
		if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, "SET ROLE ddp_owner"); err != nil {
		t.Fatal(err)
	}
	saved, err := Record(ctx, conn, cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	var principal string
	if err := conn.QueryRow(ctx, "SELECT principal FROM ddp.audit WHERE action='deploy.record' ORDER BY occurred_at DESC LIMIT 1").Scan(&principal); err != nil || principal == "" {
		t.Fatalf("audit principal=%q err=%v", principal, err)
	}
	opts.Status, opts.Phase = "succeeded", "ready"
	succeeded, err := Record(ctx, conn, cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := Latest(ctx, conn)
	if err != nil || latest == nil || latest.ID != succeeded.ID || latest.ID == saved.ID {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	opts.Phase = "startup"
	if _, err := Record(ctx, conn, cfg, opts); err == nil {
		t.Fatal("success before readiness accepted")
	}
}
