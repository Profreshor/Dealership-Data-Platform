package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestBootstrapAuditFailureIsReachedAndRollsBack(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_auth_verify_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
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

	config, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	owner, err := pgxpool.NewWithConfig(ctx, config)
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

	apiConfig := config.Copy()
	apiConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{"ddp_api"}.Sanitize())
		return err
	}
	api, err := pgxpool.NewWithConfig(ctx, apiConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	if _, err := owner.Exec(ctx, `CREATE FUNCTION app.reject_bootstrap_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private audit failure'; END $$; CREATE TRIGGER reject_bootstrap_audit BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION app.reject_bootstrap_audit()`); err != nil {
		t.Fatal(err)
	}
	err = New(api, time.Hour).Bootstrap(ctx, "reached@example.test", "reached-password")
	if err == nil || err.Error() != "record operation audit" || strings.Contains(err.Error(), "private audit failure") || strings.Contains(err.Error(), "reached@example.test") {
		t.Fatalf("audit failure was not safely returned: %v", err)
	}
	var users, audits int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE email='reached@example.test'`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM ddp.audit WHERE action='users.bootstrap'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if users != 0 || audits != 0 {
		t.Fatalf("audit failure committed state: users=%d audits=%d", users, audits)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO app.users(id,email,password_hash,is_admin) VALUES('existing','private@example.test','unused',false)`); err != nil {
		t.Fatal(err)
	}
	if err := New(api, time.Hour).Bootstrap(ctx, "private@example.test", "private-password"); err == nil || err.Error() != "administrator account already exists" {
		t.Fatalf("database failure exposed row data: %v", err)
	}

	readonlyConfig := config.Copy()
	readonlyConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{"ddp_readonly"}.Sanitize())
		return err
	}
	readonly, err := pgxpool.NewWithConfig(ctx, readonlyConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if err := New(readonly, time.Hour).Bootstrap(ctx, "readonly@example.test", "readonly-password"); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("readonly bootstrap error=%v, want audit.ErrRefused", err)
	}
}
