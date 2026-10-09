package health

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClientSQLContractAdversarial(t *testing.T) {
	pool := healthDB(t)
	root := t.TempDir()
	writeHealthQuery := func(t *testing.T, sql string) string {
		t.Helper()
		name := strings.ReplaceAll(t.Name(), "/", "_") + ".sql"
		if err := os.WriteFile(filepath.Join(root, name), []byte(sql), 0600); err != nil {
			t.Fatal(err)
		}
		return name
	}
	run := func(t *testing.T, sql string) Observation {
		t.Helper()
		cfg := &config.Config{Health: map[string]config.Health{
			"probe": {Kind: "sql", SQL: writeHealthQuery(t, sql), Severity: "warning"},
		}}
		got := Client(t.Context(), pool, cfg, root, time.Now())
		if len(got) != 1 {
			t.Fatalf("observations = %#v", got)
		}
		return got[0]
	}

	t.Run("columns may be in any order", func(t *testing.T) {
		o := run(t, `SELECT 'details'::text AS message, '42'::text AS value, true AS ok`)
		if o.State != "ok" || o.Message != "details" || o.Value != "42" {
			t.Fatalf("observation = %#v", o)
		}
	})
	t.Run("SQL text is left to Postgres", func(t *testing.T) {
		o := run(t, `SELECT 'DELETE FROM x; -- still text'::text AS message, true AS ok /* UPDATE x; */;`)
		if o.State != "ok" || o.Message != "DELETE FROM x; -- still text" {
			t.Fatalf("observation = %#v", o)
		}
		o = run(t, "SELECT true AS ok -- DELETE FROM x;")
		if o.State != "ok" {
			t.Fatalf("EOF line comment observation = %#v", o)
		}
	})

	invalid := map[string]string{
		"no rows":            `SELECT true AS ok WHERE false`,
		"two rows":           `SELECT true AS ok FROM generate_series(1, 2)`,
		"null ok":            `SELECT NULL::boolean AS ok`,
		"missing ok":         `SELECT 'yes'::text AS message`,
		"extra column":       `SELECT true AS ok, 'x'::text AS extra`,
		"duplicate column":   `SELECT true AS ok, false AS ok`,
		"nonboolean ok":      `SELECT 'true'::text AS ok`,
		"nontext message":    `SELECT true AS ok, 1 AS message`,
		"oversize message":   `SELECT true AS ok, repeat('x', 2049)::text AS message`,
		"oversize value":     `SELECT true AS ok, repeat('x', 513)::text AS value`,
		"provider SQL error": `SELECT secret_payload FROM relation_that_does_not_exist`,
	}
	for name, sql := range invalid {
		t.Run(name, func(t *testing.T) {
			o := run(t, sql)
			if o.State != "unknown" || o.Message == "" {
				t.Fatalf("observation = %#v", o)
			}
			if strings.Contains(o.Message, "secret_payload") || strings.Contains(o.Message, "relation_that_does_not_exist") {
				t.Fatalf("provider details leaked: %#v", o)
			}
		})
	}
}

func TestClientSQLIsReadOnlyEvenThroughFunction(t *testing.T) {
	pool := healthDB(t)
	if _, err := pool.Exec(t.Context(), `
		CREATE TABLE public.health_side_effects (n integer);
		CREATE FUNCTION public.health_mutate() RETURNS boolean LANGUAGE plpgsql AS $$
		BEGIN INSERT INTO public.health_side_effects VALUES (1); RETURN true; END
		$$`); err != nil {
		t.Fatal(err)
	}
	o := runHealthSQL(t, pool, `SELECT public.health_mutate() AS ok`)
	if o.State != "unknown" {
		t.Fatalf("observation = %#v", o)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM public.health_side_effects`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("side effects = %d, err = %v", count, err)
	}
}

func TestClientSQLStatementTimeout(t *testing.T) {
	pool := healthDB(t)
	started := time.Now()
	o := runHealthSQL(t, pool, `SELECT true AS ok FROM (SELECT pg_sleep(10)) AS delayed`)
	elapsed := time.Since(started)
	if o.State != "unknown" {
		t.Fatalf("observation = %#v", o)
	}
	if elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatalf("statement timeout took %v", elapsed)
	}
}

func TestReadHealthSQLBoundariesAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	valid := []byte("SELECT true AS ok")
	valid = append(valid, []byte(strings.Repeat(" ", maxSQLSize-len(valid)))...)
	if err := os.WriteFile(filepath.Join(root, "exact.sql"), valid, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readHealthSQL(root, "exact.sql"); err != nil || len(got) != maxSQLSize {
		t.Fatalf("exact limit: len=%d err=%v", len(got), err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.sql"), append(valid, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHealthSQL(root, "large.sql"); err == nil {
		t.Fatal("accepted oversized SQL file")
	}
	if _, err := readHealthSQL(root, "../outside.sql"); err == nil {
		t.Fatal("accepted path outside project root")
	}
	outside := filepath.Join(t.TempDir(), "outside.sql")
	if err := os.WriteFile(outside, []byte("SELECT true AS ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.sql")); err != nil {
		t.Fatal(err)
	}
	if _, err := readHealthSQL(root, "escape.sql"); err == nil {
		t.Fatal("accepted symlink outside project root")
	}
	fifo := filepath.Join(root, "query.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readHealthSQL(root, "query.fifo")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted FIFO as SQL")
		}
	case <-time.After(time.Second):
		t.Fatal("opening FIFO blocked before regular-file validation")
	}
}

func TestClientFreshnessEdges(t *testing.T) {
	pool := healthDB(t)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE public.freshness_probe (observed_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	run := func(maxAge string) Observation {
		t.Helper()
		cfg := &config.Config{Health: map[string]config.Health{
			"probe": {Kind: "freshness", Target: "table/public.freshness_probe", Column: "observed_at", MaxAge: maxAge, Severity: "warning"},
		}}
		return Client(t.Context(), pool, cfg, t.TempDir(), now)[0]
	}
	if o := run("1h"); o.State != "failing" {
		t.Fatalf("empty relation = %#v", o)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO public.freshness_probe VALUES (NULL)`); err != nil {
		t.Fatal(err)
	}
	if o := run("1h"); o.State != "failing" {
		t.Fatalf("null maximum = %#v", o)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO public.freshness_probe VALUES ($1)`, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if o := run("1h"); o.State != "unknown" {
		t.Fatalf("future timestamp = %#v", o)
	}
	if o := run("0s"); o.State != "unknown" {
		t.Fatalf("nonpositive max_age = %#v", o)
	}
}

func runHealthSQL(t *testing.T, pool *pgxpool.Pool, sql string) Observation {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "probe.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Health: map[string]config.Health{
		"probe": {Kind: "sql", SQL: "probe.sql", Severity: "warning"},
	}}
	return Client(context.Background(), pool, cfg, root, time.Now())[0]
}
