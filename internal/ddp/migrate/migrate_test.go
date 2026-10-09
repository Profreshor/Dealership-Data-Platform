package migrate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/migrations"
	"github.com/jackc/pgx/v5"
)

func TestMigrationChainAgainstPostgres(t *testing.T) {
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
	name := fmt.Sprintf("ddp_migrate_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+name)
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	entries, err := Status(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(all(migrations.FS)) || entries[0].Applied {
		t.Fatalf("unexpected pending status: %#v", entries)
	}
	if err := Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE ddp_owner"); err != nil {
		t.Fatal(err)
	}
	// An upgrade preprovisions the cluster role; table grants still apply as owner.
	if _, err := conn.Exec(ctx, `DELETE FROM ddp.platform_migrations WHERE id='20260905130000_backup_role'; REVOKE INSERT, UPDATE ON ops.backups FROM ddp_backup`); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var backupGrant bool
	if err := conn.QueryRow(ctx, `SELECT has_column_privilege('ddp_backup','ops.backups','status','INSERT')`).Scan(&backupGrant); err != nil || !backupGrant {
		t.Fatalf("owner upgrade omitted backup grants: %v %v", backupGrant, err)
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatal(err)
	}
	other, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	if err := Up(short, other); err == nil {
		t.Fatal("migration did not wait for advisory lock")
	}
	cancel()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil {
		t.Fatal(err)
	}
	failed := migration{kind: "app", id: "20990101000000_failed", checksum: "test", sql: []byte("CREATE TABLE app.must_rollback (id int); SELECT 1/0;")}
	if err := applyPending(ctx, conn, []migration{failed}); err == nil {
		t.Fatal("accepted failed migration")
	}
	var remains bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('app.must_rollback') IS NOT NULL").Scan(&remains); err != nil || remains {
		t.Fatalf("failed migration left table: %v %v", remains, err)
	}
	// A newer image uses the same runner to append compatible migrations.
	// This binary must remain usable after the image is rolled back.
	for _, kind := range []string{"ddp", "app"} {
		sql := []byte("CREATE TABLE app.rollback_" + kind + " (id int);")
		forward := migration{kind: kind, id: "20990101000000_forward", checksum: fmt.Sprintf("%x", sha256.Sum256(sql)), sql: sql}
		if err := applyPending(ctx, conn, []migration{forward}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Up(ctx, conn); err != nil {
		t.Fatalf("previous image rejected forward migrations: %v", err)
	}
	for _, kind := range []string{"ddp", "app"} {
		var count int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+ledger(kind)+" WHERE id='20990101000000_forward'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("rollback changed forward history: %d %v", count, err)
		}
	}
	// A renamed last migration can sort later without being a forward step.
	var latest string
	for _, m := range all(migrations.FS) {
		if m.kind == "ddp" {
			latest = m.id[:14]
		}
	}
	for _, id := range []string{"20991301000000_invalid", "bad_id", latest + "_zz_renamed"} {
		if _, err := conn.Exec(ctx, "INSERT INTO ddp.platform_migrations(id,checksum) VALUES($1,'invalid')", id); err != nil {
			t.Fatal(err)
		}
		if _, err := Status(ctx, conn); err == nil || !strings.Contains(err.Error(), "unknown applied migration:") {
			t.Fatalf("accepted invalid or historical ID %s: %v", id, err)
		}
		if _, err := conn.Exec(ctx, "DELETE FROM ddp.platform_migrations WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, "INSERT INTO ddp.client_migrations(id,checksum) VALUES('19990101000000_orphan','orphan')"); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, conn); err == nil {
		t.Fatal("accepted removed migration")
	}
	if _, err := conn.Exec(ctx, "DELETE FROM ddp.client_migrations WHERE id='19990101000000_orphan'"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE ddp_api"); err != nil {
		t.Fatal(err)
	}
	entries, err = Status(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(all(migrations.FS)) {
		t.Fatalf("unexpected migration status: %#v", entries)
	}
	if _, err := conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "UPDATE ddp.platform_migrations SET checksum='drift' WHERE id=$1", "20260904000000_baseline"); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(ctx, conn); err == nil {
		t.Fatal("checksum drift was accepted")
	}
	if err := Up(ctx, conn); err == nil {
		t.Fatal("migration startup accepted checksum drift")
	}
}
