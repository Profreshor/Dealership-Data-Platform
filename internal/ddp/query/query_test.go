package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
)

func TestFingerprintAndKeywordGuard(t *testing.T) {
	if Fingerprint("select 1") != "822ae07d4783158bc1912bb623e5107cc9002d519e1143a9c200ed6ee18b6d0f" {
		t.Fatal("fingerprint changed")
	}
	for _, statement := range []string{" /* outer /* nested */ */ -- comment\n ( SELECT 1)", "WITH x AS (SELECT 1) SELECT * FROM x"} {
		if err := guard(statement, false); err != nil {
			t.Fatalf("read guard rejected %q: %v", statement, err)
		}
	}
	for _, statement := range []string{"BEGIN", "/*x*/ SET statement_timeout=1", "COPY t FROM STDIN"} {
		if !errors.Is(guard(statement, true), ErrInvalid) {
			t.Fatalf("control statement accepted: %q", statement)
		}
	}
}

func queryDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ddp_query_%d", time.Now().UnixNano())
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(t.Context())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})
	cfg := admin.Config().Copy()
	cfg.Database = name
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if err := migrate.Up(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestRunRealPostgresValuesBoundsAndReadOnly(t *testing.T) {
	conn := queryDatabase(t)
	if _, err := conn.Exec(t.Context(), "CREATE TABLE app.query_probe(id int primary key, value text)"); err != nil {
		t.Fatal(err)
	}
	result, err := Run(t.Context(), conn, "SELECT NULL::text AS same, 7::int AS same, E'\\x41'::bytea AS bytes, DATE '2026-01-02' AS day", Options{Limit: 2, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != 4 || len(result.Rows) != 1 || result.Rows[0][0] != nil || *result.Rows[0][1] != "7" || *result.Rows[0][2] != "\\x41" || *result.Rows[0][3] != "2026-01-02" {
		t.Fatalf("raw result lost: %#v", result)
	}
	if _, err := Run(t.Context(), conn, "INSERT INTO app.query_probe VALUES (1,'x')", Options{Limit: 2, Timeout: time.Second}); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("read-only DML: %v", err)
	}
	if _, err := Run(t.Context(), conn, "SELECT 1; SELECT 2", Options{Limit: 2, Timeout: time.Second}); err == nil {
		t.Fatal("accepted multiple statements")
	}
	if _, err := Run(t.Context(), conn, "SELECT generate_series(1,3)", Options{Limit: 2, Timeout: time.Second}); !errors.Is(err, ErrLimit) {
		t.Fatalf("row limit: %v", err)
	}
}

func TestValidation(t *testing.T) {
	if err := validate("", Options{Limit: 1, Timeout: time.Second}); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty SQL accepted")
	}
	if err := validate("select 1", Options{Limit: 0, Timeout: time.Second}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid limit accepted")
	}
	if err := validate("select 1", Options{Limit: 1, Timeout: 6 * time.Minute}); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid timeout accepted")
	}
	if err := guard("INSERT INTO x VALUES (1)", false); !errors.Is(err, audit.ErrRefused) {
		t.Fatal("read-only DML accepted")
	}
}

func TestWriteFailureBoundaries(t *testing.T) {
	admin := queryDatabase(t)
	ctx := t.Context()
	_, err := admin.Exec(ctx, `
CREATE TABLE mart.query_probe(id integer PRIMARY KEY, value text);
GRANT SELECT ON mart.query_probe TO ddp_readonly;
GRANT SELECT, INSERT ON mart.query_probe TO ddp_job;
CREATE FUNCTION mart.query_mutate() RETURNS integer LANGUAGE plpgsql AS $$
BEGIN INSERT INTO mart.query_probe VALUES (99, 'hidden'); RETURN 99; END $$;
CREATE FUNCTION ddp.reject_sql_success() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.action = 'sql.write' AND NEW.outcome->>'status' = 'succeeded'
     AND EXISTS (SELECT FROM mart.query_probe WHERE id = 13) THEN
    RAISE EXCEPTION 'private-value';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER reject_sql_success BEFORE INSERT ON ddp.audit
FOR EACH ROW EXECUTE FUNCTION ddp.reject_sql_success();`)
	if err != nil {
		t.Fatal(err)
	}
	connect := func(t *testing.T, role string, simple bool) *pgx.Conn {
		t.Helper()
		cfg := admin.Config().Copy()
		if role != "" {
			cfg.RuntimeParams["role"] = role
		}
		if simple {
			cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		}
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}
	checkAudit := func(t *testing.T, sql, status string) {
		t.Helper()
		var count int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM ddp.audit
WHERE action='sql.write' AND target=$1 AND outcome->>'status'=$2
AND principal=session_user AND outcome::text NOT LIKE '%private-value%'`,
			"sql/"+Fingerprint(sql), status).Scan(&count); err != nil || count != 1 {
			t.Fatalf("expected one safe %s audit: count=%d err=%v", status, count, err)
		}
	}
	t.Run("success and duplicate failure", func(t *testing.T) {
		conn := connect(t, "", false)
		sql := "INSERT INTO mart.query_probe VALUES (1, 'safe') RETURNING id"
		result, err := Run(ctx, conn, sql, Options{Write: true, Limit: 1, Timeout: time.Second})
		if err != nil || result.Command != "INSERT 0 1" || result.RowsAffected != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		checkAudit(t, sql, "succeeded")
		sql = "INSERT INTO mart.query_probe VALUES (1, 'private-value')"
		if _, err := Run(ctx, conn, sql, Options{Write: true, Limit: 1, Timeout: time.Second}); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("unsafe failure: %v", err)
		}
		checkAudit(t, sql, "failed")
	})
	t.Run("rejected success audit rolls back mutation and records failure", func(t *testing.T) {
		sql := "INSERT INTO mart.query_probe VALUES (13, 'private-value')"
		if _, err := Run(ctx, connect(t, "", false), sql, Options{Write: true, Limit: 1, Timeout: time.Second}); err == nil || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("unsafe audit failure: %v", err)
		}
		checkAudit(t, sql, "failed")
	})
	for _, sql := range []string{
		"WITH changed AS (INSERT INTO mart.query_probe VALUES (99, 'hidden') RETURNING id) SELECT * FROM changed",
		"SELECT mart.query_mutate()",
	} {
		t.Run(sql, func(t *testing.T) {
			if _, err := Run(ctx, connect(t, "", false), sql, Options{Limit: 2, Timeout: time.Second}); !errors.Is(err, audit.ErrRefused) {
				t.Fatalf("hidden DML was not refused: %v", err)
			}
		})
	}
	t.Run("simple protocol cannot enable multiple statements", func(t *testing.T) {
		sql := "INSERT INTO mart.query_probe VALUES (98, 'hidden'); SELECT 1"
		if _, err := Run(ctx, connect(t, "", true), sql, Options{Write: true, Limit: 1, Timeout: time.Second}); err == nil {
			t.Fatal("multiple statements accepted")
		}
		checkAudit(t, sql, "failed")
	})
	for _, tc := range []struct{ name, sql string }{
		{"row limit", "INSERT INTO mart.query_probe SELECT n, 'rows' FROM generate_series(20,22) n RETURNING id"},
		{"byte limit", "INSERT INTO mart.query_probe VALUES (23, repeat('x', 4194305)) RETURNING value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Run(ctx, connect(t, "", false), tc.sql, Options{Write: true, Limit: 1, Timeout: time.Second})
			if !errors.Is(err, ErrLimit) || len(result.Rows) != 0 {
				t.Fatalf("partial result or missing limit error: %+v %v", result, err)
			}
		})
	}
	t.Run("timeout rolls back function DML", func(t *testing.T) {
		_, err := admin.Exec(ctx, `CREATE FUNCTION mart.query_slow() RETURNS void LANGUAGE plpgsql AS $$
BEGIN INSERT INTO mart.query_probe VALUES (24, 'slow'); PERFORM pg_sleep(10); END $$`)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if _, err := Run(ctx, connect(t, "", false), "SELECT mart.query_slow()", Options{Write: true, Limit: 1, Timeout: 50 * time.Millisecond}); err == nil || time.Since(started) > 6*time.Second {
			t.Fatalf("timeout was not bounded: %v", err)
		}
	})
	t.Run("data grant does not bypass audit grant", func(t *testing.T) {
		sql := "INSERT INTO mart.query_probe VALUES (25, 'unaudited')"
		if _, err := Run(ctx, connect(t, "ddp_job", false), sql, Options{Write: true, Limit: 1, Timeout: time.Second}); !errors.Is(err, audit.ErrRefused) || !strings.Contains(err.Error(), "audit could not be recorded") {
			t.Fatalf("audit denial was not reported: %v", err)
		}
	})
	t.Run("readonly grant refuses confirmed writes", func(t *testing.T) {
		conn := connect(t, "ddp_readonly", false)
		if _, err := Run(ctx, conn, "SELECT * FROM mart.query_probe", Options{Limit: 10, Timeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(ctx, conn, "INSERT INTO mart.query_probe VALUES (26, 'denied')", Options{Write: true, Limit: 1, Timeout: time.Second}); !errors.Is(err, audit.ErrRefused) {
			t.Fatalf("write was not refused: %v", err)
		}
	})
	t.Run("changed principal is refused", func(t *testing.T) {
		sql := "SELECT set_config('role', 'ddp_readonly', true)"
		if _, err := Run(ctx, connect(t, "", false), sql, Options{Write: true, Limit: 1, Timeout: time.Second}); !errors.Is(err, audit.ErrRefused) {
			t.Fatalf("changed principal: %v", err)
		}
		checkAudit(t, sql, "failed")
	})
	var count int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM mart.query_probe WHERE id <> 1").Scan(&count); err != nil || count != 0 {
		t.Fatalf("a failed operation committed data: %d %v", count, err)
	}
}
