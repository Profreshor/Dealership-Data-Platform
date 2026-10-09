package models

import (
	"context"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestMaterializedLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	root := t.TempDir()
	if _, err := pool.Exec(ctx, `CREATE TABLE app.cached_source (id integer, label text); INSERT INTO app.cached_source VALUES (1,'old')`); err != nil {
		t.Fatal(err)
	}
	c := contract(map[string]config.Column{"id": {Type: "integer"}, "label": {Type: "text"}}, nil, []string{"id"})
	cfg := &config.Config{Models: map[string]config.Model{
		"staging.cached": {File: "staging.sql", Materialization: "view", Reads: []string{"table/app.cached_source"}, Contract: c},
		"core.cached":    {File: "core.sql", Materialization: "materialized_view", Reads: []string{"model/staging.cached"}, Contract: c},
		"mart.cached":    {File: "mart.sql", Materialization: "view", Reads: []string{"model/core.cached"}, Contract: c},
	}}
	writeSQL(t, root, "staging.sql", `SELECT id, label FROM app.cached_source`)
	writeSQL(t, root, "core.sql", `SELECT id, label FROM staging.cached`)
	writeSQL(t, root, "mart.sql", `SELECT id, label FROM core.cached`)
	results, err := Apply(ctx, pool, cfg, root)
	if err != nil || len(results) != 3 {
		t.Fatalf("apply: %+v %v", results, err)
	}
	if err := Verify(ctx, pool, cfg); err != nil {
		t.Fatal(err)
	}
	var oid uint32
	if err := pool.QueryRow(ctx, `SELECT 'core.cached'::regclass::oid`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.cached_source SET label='new'`); err != nil {
		t.Fatal(err)
	}
	assertLabel := func(want string) {
		t.Helper()
		var label string
		if err := pool.QueryRow(ctx, `SELECT label FROM mart.cached`).Scan(&label); err != nil || label != want {
			t.Fatalf("cached label = %q, want %q (%v)", label, want, err)
		}
	}
	assertLabel("old")
	// Postgres normalization makes formatting-only changes an in-place refresh,
	// including when the materialized view has a dependent model.
	writeSQL(t, root, "core.sql", "-- formatting\n select id, label\n from staging.cached;\n")
	if result, err := Refresh(ctx, pool, cfg, root, "model/core.cached"); err != nil || result.Status != "succeeded" {
		t.Fatalf("refresh: %+v %v", result, err)
	}
	assertLabel("new")
	var after uint32
	if err := pool.QueryRow(ctx, `SELECT 'core.cached'::regclass::oid`).Scan(&after); err != nil || after != oid {
		t.Fatalf("refresh replaced relation: %d != %d (%v)", after, oid, err)
	}
	writeSQL(t, root, "core.sql", `SELECT id, upper(label) AS label FROM staging.cached`)
	if _, err := Refresh(ctx, pool, cfg, root, "model/core.cached"); err == nil || !strings.Contains(err.Error(), "dependent relations") {
		t.Fatalf("expected restrictive replacement failure: %v", err)
	}
	assertLabel("new")
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM ops.model_refreshes WHERE model_ref='model/core.cached' ORDER BY started_at DESC LIMIT 1`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("refresh history: %q %v", status, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.cached_source SET label='new'; DROP VIEW mart.cached`); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(ctx, pool, cfg, root, "model/core.cached"); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(ctx, pool, cfg, root, "model/mart.cached"); err != nil {
		t.Fatal(err)
	}
	assertLabel("NEW")
	for _, role := range []string{"ddp_job", "ddp_api", "ddp_readonly"} {
		assertPrivilege(t, pool, role, "core.cached", true)
	}
	var leftovers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname LIKE '_ddp_candidate_%'`).Scan(&leftovers); err != nil || leftovers != 0 {
		t.Fatalf("comparison relation leaked: %d %v", leftovers, err)
	}
	if _, err := Refresh(ctx, pool, cfg, root, "model/missing"); err == nil {
		t.Fatal("unknown model accepted")
	}
	// Invalid SQL must never remove the last usable materialized contents.
	writeSQL(t, root, "core.sql", `SELECT id FROM staging.cached; DROP VIEW mart.cached`)
	if _, err := Refresh(ctx, pool, cfg, root, "model/core.cached"); err == nil {
		t.Fatal("multiple statements accepted")
	}
	assertLabel("NEW")
}

func TestRefreshRollsBackQueryFailureAndLeavesDataChecksToVerify(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	root := t.TempDir()
	if _, err := pool.Exec(ctx, `CREATE TABLE app.refresh_input (value text); INSERT INTO app.refresh_input VALUES ('1')`); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Models: map[string]config.Model{
		"core.refresh_test": {File: "model.sql", Materialization: "materialized_view", Reads: []string{"table/app.refresh_input"}, Contract: contract(map[string]config.Column{"id": {Type: "integer"}}, nil, []string{"id"})},
	}}
	writeSQL(t, root, "model.sql", `SELECT value::integer AS id FROM app.refresh_input`)
	if _, err := Apply(ctx, pool, cfg, root); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.refresh_input SET value='private-invalid-value'`); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(ctx, pool, cfg, root, "model/core.refresh_test"); err == nil || !strings.Contains(err.Error(), "22P02") || strings.Contains(err.Error(), "private-invalid-value") {
		t.Fatalf("unsafe or missing query error: %v", err)
	}
	var id int
	if err := pool.QueryRow(ctx, `SELECT id FROM core.refresh_test`).Scan(&id); err != nil || id != 1 {
		t.Fatalf("last usable rows lost: %d %v", id, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.refresh_input SET value='2'; INSERT INTO app.refresh_input VALUES ('2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(ctx, pool, cfg, root, "model/core.refresh_test"); err != nil {
		t.Fatalf("refresh performed semantic scan: %v", err)
	}
	if err := Verify(ctx, pool, cfg); err == nil || !strings.Contains(err.Error(), "unique key contains duplicate") {
		t.Fatalf("verification missed duplicate identity: %v", err)
	}
}
