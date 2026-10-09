package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/query"
	"github.com/jackc/pgx/v5"
)

func TestSQLCLIAgainstPostgres(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ddp_sql_cli_%d", time.Now().UnixNano())
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(t.Context())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
		_ = admin.Close(ctx)
	})
	uri, err := url.Parse(raw)
	if err != nil || (uri.Scheme != "postgres" && uri.Scheme != "postgresql") {
		t.Fatal("TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	uri.Path = "/" + name
	conn, err := pgx.Connect(t.Context(), uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if err := migrate.Up(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "CREATE TABLE mart.sql_probe(id integer PRIMARY KEY); GRANT SELECT ON mart.sql_probe TO ddp_readonly"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", uri.String())
	run := func(sql string, want int, flags ...string) query.Result {
		t.Helper()
		var out, logs bytes.Buffer
		args := append([]string{"sql", sql, "--json", "--config", "does-not-exist"}, flags...)
		code := Execute(t.Context(), args, &out, &logs, nil)
		var result struct {
			OK   bool
			Data query.Result
		}
		if code != want || json.Unmarshal(out.Bytes(), &result) != nil || result.OK != (want == 0) || logs.Len() != 0 {
			t.Fatalf("code=%d want=%d out=%s logs=%s", code, want, &out, &logs)
		}
		if want != 0 && strings.Contains(out.String(), "private-value") {
			t.Fatal("SQL value leaked in error")
		}
		return result.Data
	}
	result := run("SELECT 9007199254740993::numeric AS duplicate, NULL::text AS duplicate", 0)
	if len(result.Columns) != 2 || result.Columns[0].Name != "duplicate" || result.Columns[1].Name != "duplicate" || len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != nil {
		t.Fatalf("result lost text values: %+v", result)
	}
	statement := "INSERT INTO mart.sql_probe VALUES (1) RETURNING id"
	run(statement, 4)
	run(statement, 0, "--write", "--confirm", query.Fingerprint(statement))
	var audited bool
	if err := conn.QueryRow(t.Context(), "SELECT EXISTS(SELECT FROM ddp.audit WHERE action='sql.write' AND target=$1 AND principal=session_user AND outcome->>'status'='succeeded')", "sql/"+query.Fingerprint(statement)).Scan(&audited); err != nil || !audited {
		t.Fatalf("write audit: %t %v", audited, err)
	}
	bad := "SELECT 'private-value'::integer"
	run(bad, 1, "--write", "--confirm", query.Fingerprint(bad))
	q := uri.Query()
	q.Set("role", "ddp_readonly")
	uri.RawQuery = q.Encode()
	t.Setenv("DATABASE_URL", uri.String())
	run("SELECT * FROM mart.sql_probe", 0)
	run("SELECT * FROM app.sessions", 4)
	statement = "INSERT INTO mart.sql_probe VALUES (2)"
	run(statement, 4, "--write", "--confirm", query.Fingerprint(statement))

	var count int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM mart.sql_probe").Scan(&count); err != nil || count != 1 {
		t.Fatalf("readonly changed data: count=%d err=%v", count, err)
	}
}
