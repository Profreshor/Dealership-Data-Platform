package migrate

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestProvisionAgainstPostgres(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	// Fixed production names need one test owner on this disposable cluster.
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock(1245793106, 2)"); err != nil {
		t.Fatal(err)
	}
	components := []string{"owner", "scheduler", "job", "api", "backup", "readonly"}
	for _, component := range components {
		var exists bool
		if err := admin.QueryRow(ctx, "SELECT EXISTS(SELECT FROM pg_roles WHERE rolname=$1)", "ddp_"+component+"_login").Scan(&exists); err != nil || exists {
			t.Fatalf("provision test requires unused production login names on a disposable cluster: %s %v", component, err)
		}
	}
	name := fmt.Sprintf("ddp_provision_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		for _, component := range components {
			if _, err := admin.Exec(context.Background(), "DROP ROLE IF EXISTS ddp_"+component+"_login"); err != nil {
				t.Error(err)
			}
		}
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	password, rotated := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	if _, err := Provision(ctx, u.String(), "api", password); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("missing migrations: %v", err)
	}
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if err := Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "REVOKE CONNECT ON DATABASE "+name+" FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	// Test group drift without changing the cluster state seen by other packages.
	for _, drift := range []struct{ group, sql string }{
		{"ddp_api", "ALTER ROLE ddp_api LOGIN"},
		{"ddp_api", "GRANT ddp_owner TO ddp_api"},
		{"ddp_backup", "GRANT pg_read_all_data TO ddp_backup WITH ADMIN TRUE"},
	} {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, drift.sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		err = provisionLogin(ctx, tx, ProvisionResult{Group: drift.group, Login: drift.group + "_login"}, password)
		_ = tx.Rollback(ctx)
		if !errors.Is(err, audit.ErrRefused) {
			t.Fatalf("accepted group drift %s: %v", drift.sql, err)
		}
	}
	loginURL := func(component, secret string) string {
		copy := *u
		copy.User = url.UserPassword("ddp_"+component+"_login", secret)
		return copy.String()
	}
	connect := func(component, secret string) *pgx.Conn {
		t.Helper()
		conn, err := pgx.Connect(ctx, loginURL(component, secret))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	for _, component := range components {
		for range 2 {
			result, err := Provision(ctx, u.String(), component, password)
			if err != nil || result.Login != "ddp_"+component+"_login" || result.Group != "ddp_"+component {
				t.Fatalf("provision %s: %+v %v", component, result, err)
			}
		}
		login := connect(component, password)
		var role, session string
		if err := login.QueryRow(ctx, "SELECT current_user,session_user").Scan(&role, &session); err != nil {
			t.Fatal(err)
		}
		if session != "ddp_"+component+"_login" || role != "ddp_"+component {
			t.Fatalf("login authority: %s %s", role, session)
		}
		if component == "owner" {
			if err := Up(ctx, login); err != nil {
				t.Fatalf("owner migration: %v", err)
			}
			if _, err := login.Exec(ctx, "CREATE TABLE app.provision_probe(id int)"); err != nil {
				t.Fatal(err)
			}
			var owner string
			if err := conn.QueryRow(ctx, "SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='app.provision_probe'::regclass").Scan(&owner); err != nil || owner != "ddp_owner" {
				t.Fatalf("migration object owner: %s %v", owner, err)
			}
		} else {
			for _, sql := range []string{"CREATE TABLE app.forbidden(id int)", "CREATE ROLE provision_forbidden", "SET ROLE ddp_owner"} {
				_, err := login.Exec(ctx, sql)
				var pe *pgconn.PgError
				if !errors.As(err, &pe) || (pe.Code != "42501" && pe.Code != "0LP01") {
					t.Fatalf("%s unexpectedly allowed %s: %v", component, sql, err)
				}
			}
		}
		var scram bool
		if err := conn.QueryRow(ctx, "SELECT rolpassword LIKE 'SCRAM-SHA-256$%' FROM pg_authid WHERE rolname=$1", session).Scan(&scram); err != nil || !scram {
			t.Fatalf("SCRAM verifier: %t %v", scram, err)
		}
	}
	if _, err := Provision(ctx, loginURL("owner", password), "api", rotated); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("owner login can provision: %v", err)
	}
	for _, drift := range []struct{ set, reset string }{
		{"ALTER ROLE ddp_api_login CREATEDB", "ALTER ROLE ddp_api_login NOCREATEDB"},
		{"GRANT ddp_owner TO ddp_api_login", "REVOKE ddp_owner FROM ddp_api_login"},
		{"ALTER ROLE ddp_api_login SET search_path TO app", "ALTER ROLE ddp_api_login RESET search_path"},
		{"GRANT UPDATE ON app.provision_probe TO ddp_api_login", "REVOKE UPDATE ON app.provision_probe FROM ddp_api_login"},
	} {
		if _, err := conn.Exec(ctx, drift.set); err != nil {
			t.Fatal(err)
		}
		_, provisionErr := Provision(ctx, u.String(), "api", rotated)
		if _, err := conn.Exec(ctx, drift.reset); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(provisionErr, audit.ErrRefused) {
			t.Fatalf("accepted drift %s: %v", drift.set, provisionErr)
		}
		_ = connect("api", password).Close(ctx)
	}
	if _, err := conn.Exec(ctx, `CREATE FUNCTION app.refuse_provision_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
IF NEW.action='database.provision' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END$$;
CREATE TRIGGER refuse_provision BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION app.refuse_provision_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(ctx, u.String(), "api", rotated); err == nil {
		t.Fatal("credential change escaped audit rollback")
	}
	_ = connect("api", password).Close(ctx)
	if _, err := conn.Exec(ctx, "DROP TRIGGER refuse_provision ON ddp.audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(ctx, u.String(), "api", rotated); err != nil {
		t.Fatal(err)
	}
	_ = connect("api", rotated).Close(ctx)
	if old, err := pgx.Connect(ctx, loginURL("api", password)); err == nil {
		_ = old.Close(ctx)
		t.Fatal("old password still authenticates after rotation")
	}
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM ddp.audit WHERE action='database.provision'").Scan(&count); err != nil || count != 13 {
		t.Fatalf("provision audit count: %d %v", count, err)
	}
}
