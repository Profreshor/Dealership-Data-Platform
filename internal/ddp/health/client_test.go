package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestClientSortsRulesAndBoundsUnavailableResults(t *testing.T) {
	got := Client(t.Context(), nil, &config.Config{Health: map[string]config.Health{
		"z": {Severity: "warning", Notify: []string{"group/ops"}},
		"a": {Severity: "critical"},
	}}, "", nowForTest())
	if len(got) != 2 || got[0].Ref != "health/a" || got[1].Ref != "health/z" {
		t.Fatalf("unexpected order: %#v", got)
	}
	if got[0].State != "unknown" || got[0].Message == "" {
		t.Fatalf("missing bounded unknown result: %#v", got[0])
	}
}

func TestClientPostgresRules(t *testing.T) {
	p := healthDB(t)
	if _, err := p.Exec(t.Context(), `CREATE SCHEMA IF NOT EXISTS app; CREATE TABLE app.health_rows (loaded_at timestamptz); INSERT INTO app.health_rows VALUES (now())`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write := func(name, sql string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(sql), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("health/false.sql", `SELECT false AS ok, 'bad'::text AS message`)
	write("health/shape.sql", `SELECT true AS ok, 1 AS value`)
	write("health/write.sql", `DELETE FROM app.health_rows RETURNING true AS ok`)
	cfg := &config.Config{Health: map[string]config.Health{
		"fresh": {Kind: "freshness", Target: "table/app.health_rows", Column: "loaded_at", MaxAge: "1h", Severity: "warning"},
		"false": {Kind: "sql", SQL: "health/false.sql", Severity: "warning"},
		"shape": {Kind: "sql", SQL: "health/shape.sql", Severity: "warning"},
		"write": {Kind: "sql", SQL: "health/write.sql", Severity: "warning"},
	}}
	databaseNow := func() time.Time {
		t.Helper()
		var now time.Time
		if err := p.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
			t.Fatal(err)
		}
		return now
	}
	got := Client(t.Context(), p, cfg, root, databaseNow())
	states := map[string]string{}
	for _, o := range got {
		states[o.Ref] = o.State
	}
	if states["health/fresh"] != "ok" || states["health/false"] != "failing" || states["health/shape"] != "unknown" || states["health/write"] != "unknown" {
		t.Fatalf("states: %#v observations: %#v", states, got)
	}
	if _, err := p.Exec(t.Context(), `UPDATE app.health_rows SET loaded_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	for _, o := range Client(t.Context(), p, cfg, root, databaseNow()) {
		if o.Ref == "health/fresh" && o.State != "failing" {
			t.Fatalf("stale state: %#v", o)
		}
	}
	if _, err := p.Exec(t.Context(), `TRUNCATE app.health_rows`); err != nil {
		t.Fatal(err)
	}
	for _, o := range Client(t.Context(), p, cfg, root, databaseNow()) {
		if o.Ref == "health/fresh" && o.State != "failing" {
			t.Fatalf("null state: %#v", o)
		}
	}
}

func nowForTest() time.Time { return time.Now() }
