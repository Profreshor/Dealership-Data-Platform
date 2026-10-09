package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/migrations"
	"github.com/jackc/pgx/v5"
)

func auditDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ddp_migration_audit_%d", time.Now().UnixNano())
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(t.Context())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(ctx)
	})
	cfg := admin.Config().Copy()
	cfg.Database = name
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func TestMigrationAuditCoversFreshChainAndDoesNotDuplicate(t *testing.T) {
	conn := auditDatabase(t)
	if err := Up(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	for _, m := range all(migrations.FS) {
		var matches int
		if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM ddp.audit WHERE principal=session_user AND action='migrate.up' AND target=$1 AND occurred_at IS NOT NULL AND outcome=jsonb_build_object('status','applied','checksum',$2::text)`, "migration/"+m.kind+"/"+m.id, m.checksum).Scan(&matches); err != nil || matches != 1 {
			t.Fatalf("migration %s/%s audit count=%d: %v", m.kind, m.id, matches, err)
		}
	}
	if _, err := conn.Exec(t.Context(), "SET ROLE ddp_owner"); err != nil {
		t.Fatal(err)
	}
	if err := Up(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM ddp.audit").Scan(&count); err != nil || count != len(all(migrations.FS)) {
		t.Fatalf("repeat or read-only status added audit: count=%d %v", count, err)
	}
}

func TestMigrationAuditFailureRollsBackChangeAndLedger(t *testing.T) {
	conn := auditDatabase(t)
	if err := Up(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `CREATE FUNCTION ddp.reject_migration_success() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='migrate.up' AND NEW.outcome->>'status'='applied' THEN RAISE EXCEPTION USING ERRCODE='42501', MESSAGE='private audit detail'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_migration_success BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION ddp.reject_migration_success()`); err != nil {
		t.Fatal(err)
	}
	m := migration{"app", "20990101000000_audit_denied", "synthetic", []byte("CREATE TABLE app.audit_rollback(id int)")}
	err := applyPending(t.Context(), conn, []migration{m})
	if !errors.Is(err, audit.ErrRefused) || strings.Contains(err.Error(), "private audit detail") {
		t.Fatalf("audit refusal: %v", err)
	}
	var exists bool
	if err := conn.QueryRow(t.Context(), `SELECT to_regclass('app.audit_rollback') IS NOT NULL OR EXISTS(SELECT FROM ddp.client_migrations WHERE id=$1)`, m.id).Scan(&exists); err != nil || exists {
		t.Fatalf("unaudited mutation committed: %t %v", exists, err)
	}
	var failed int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM ddp.audit WHERE target=$1 AND outcome='{"status":"failed","checksum":"synthetic"}' AND principal=session_user`, "migration/app/"+m.id).Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("failed operation evidence: %d %v", failed, err)
	}
}

func TestMigrationFailurePreservesEarlierCommitAndRecordsOnlySafeOutcome(t *testing.T) {
	conn := auditDatabase(t)
	if err := Up(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	good := migration{"app", "20990101000000_good", "good", []byte("CREATE TABLE app.before_failure(id int)")}
	bad := migration{"app", "20990101000001_bad", "bad", []byte("CREATE TABLE app.after_failure(id int); SELECT 'private-value'::integer;")}
	err := applyPending(t.Context(), conn, []migration{good, bad})
	if err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("unsafe migration failure: %v", err)
	}
	var correct bool
	if err := conn.QueryRow(t.Context(), `SELECT to_regclass('app.before_failure') IS NOT NULL AND to_regclass('app.after_failure') IS NULL AND (SELECT count(*) FROM ddp.client_migrations WHERE id=ANY($1))=1`, []string{good.id, bad.id}).Scan(&correct); err != nil || !correct {
		t.Fatalf("migration commit boundaries lost: %t %v", correct, err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT count(*)=2 AND bool_and(outcome=jsonb_build_object('status',CASE WHEN target=$1 THEN 'applied' ELSE 'failed' END,'checksum',CASE WHEN target=$1 THEN 'good' ELSE 'bad' END)) FROM ddp.audit WHERE target=ANY($2)`, "migration/app/"+good.id, []string{"migration/app/" + good.id, "migration/app/" + bad.id}).Scan(&correct); err != nil || !correct {
		t.Fatalf("migration outcomes missing: %t %v", correct, err)
	}
}

func TestBootstrapMigrationFailureLeavesNoUnauditedChange(t *testing.T) {
	conn := auditDatabase(t)
	if err := ensureLedgers(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	first := migration{"ddp", "20990101000000_first", "first", []byte("CREATE TABLE ddp.bootstrap_probe(id int)")}
	bad := migration{"ddp", "20990101000001_bad", "bad", []byte("SELECT 1/0")}
	if err := applyPending(t.Context(), conn, []migration{first, bad}); err == nil {
		t.Fatal("accepted failed bootstrap")
	}
	var remains bool
	if err := conn.QueryRow(t.Context(), `SELECT to_regclass('ddp.bootstrap_probe') IS NOT NULL OR EXISTS(SELECT FROM ddp.platform_migrations)`).Scan(&remains); err != nil || remains {
		t.Fatalf("bootstrap committed before audit was available: %t %v", remains, err)
	}
}
