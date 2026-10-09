package models

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestModelsAgainstPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE app.model_source (id integer, label text);
		INSERT INTO app.model_source VALUES (1, 'one'), (2, 'two')`); err != nil {
		t.Fatal(err)
	}

	t.Run("applies views in dependency order", func(t *testing.T) {
		root := t.TempDir()
		writeSQL(t, root, "models/staging/z_base.sql", `SELECT id, label FROM app.model_source`)
		writeSQL(t, root, "models/core/m_middle.sql", `SELECT id, label FROM staging.z_base`)
		writeSQL(t, root, "models/mart/a_final.sql", `SELECT id, label FROM core.m_middle`)
		cfg := &config.Config{Models: map[string]config.Model{
			"mart.a_final": {
				File: "models/mart/a_final.sql", Materialization: "view", Reads: []string{"model/core.m_middle"},
				Contract: contract(map[string]config.Column{"id": {Type: "integer"}, "label": {Type: "text"}}, []string{"id"}, nil),
			},
			"core.m_middle": {
				File: "models/core/m_middle.sql", Materialization: "view", Reads: []string{"model/staging.z_base"},
				Contract: contract(map[string]config.Column{"id": {Type: "integer"}, "label": {Type: "text"}}, nil, []string{"id"}),
			},
			"staging.z_base": {
				File: "models/staging/z_base.sql", Materialization: "view", Reads: []string{"table/app.model_source"},
				Contract: contract(map[string]config.Column{"id": {Type: "int4"}, "label": {Type: "text"}}, nil, []string{"id"}),
			},
		}}
		got, err := Apply(ctx, pool, cfg, root)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got) != `[{model/staging.z_base succeeded} {model/core.m_middle succeeded} {model/mart.a_final succeeded}]` {
			t.Fatalf("unexpected apply order: %#v", got)
		}
		if err := Verify(ctx, pool, cfg); err != nil {
			t.Fatal(err)
		}
		assertPrivilege(t, pool, "ddp_job", "staging.z_base", true)
		assertPrivilege(t, pool, "ddp_readonly", "staging.z_base", true)
		assertPrivilege(t, pool, "ddp_api", "staging.z_base", false)
		for _, role := range []string{"ddp_job", "ddp_api", "ddp_readonly"} {
			assertPrivilege(t, pool, role, "mart.a_final", true)
		}
	})

	t.Run("reads direct dependencies from Postgres", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `CREATE TABLE app.model_extra (id integer)`); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		writeSQL(t, root, "model.sql", `WITH source AS (SELECT s.id FROM app.model_source s WHERE EXISTS (SELECT 1 FROM app.model_extra e WHERE e.id = s.id)) SELECT source.id FROM source JOIN app.model_source again USING (id)`)
		cfg := &config.Config{Models: map[string]config.Model{"mart.catalog_direct": {
			File: "model.sql", Materialization: "view", Reads: []string{"table/app.model_source", "table/app.model_extra"},
			Contract: contract(map[string]config.Column{"id": {Type: "integer"}}, nil, nil),
		}}}
		if _, err := Apply(ctx, pool, cfg, root); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rejects undeclared direct dependency and preserves view", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `CREATE OR REPLACE VIEW mart.catalog_guard AS SELECT 9::integer AS id`); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		writeSQL(t, root, "model.sql", `SELECT s.id FROM app.model_source s JOIN app.model_extra e ON e.id = s.id`)
		cfg := &config.Config{Models: map[string]config.Model{"mart.catalog_guard": {
			File: "model.sql", Materialization: "view", Reads: []string{"table/app.model_source"},
			Contract: contract(map[string]config.Column{"id": {Type: "integer"}}, nil, nil),
		}}}
		if _, err := Apply(ctx, pool, cfg, root); err == nil || !strings.Contains(err.Error(), "undeclared direct dependency") {
			t.Fatalf("unexpected dependency error: %v", err)
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM ops.model_refreshes WHERE model_ref='model/mart.catalog_guard' ORDER BY started_at DESC LIMIT 1`).Scan(&status); err != nil || status != "failed" {
			t.Fatalf("dependency failure history: status=%q err=%v", status, err)
		}
		var id int
		if err := pool.QueryRow(ctx, `SELECT id FROM mart.catalog_guard`).Scan(&id); err != nil || id != 9 {
			t.Fatalf("old view was not preserved: id=%d err=%v", id, err)
		}
	})

	t.Run("verify detects dependency graph drift", func(t *testing.T) {
		root := t.TempDir()
		writeSQL(t, root, "model.sql", `SELECT id FROM app.model_source`)
		cfg := &config.Config{Models: map[string]config.Model{"mart.catalog_drift": {
			File: "model.sql", Materialization: "view", Reads: []string{"table/app.model_source"},
			Contract: contract(map[string]config.Column{"id": {Type: "integer"}}, nil, nil),
		}}}
		if _, err := Apply(ctx, pool, cfg, root); err != nil {
			t.Fatal(err)
		}
		model := cfg.Models["mart.catalog_drift"]
		model.Reads = []string{"table/app.model_extra"}
		cfg.Models["mart.catalog_drift"] = model
		if err := Verify(ctx, pool, cfg); err == nil {
			t.Fatalf("unexpected drift result: %v", err)
		}
	})

	t.Run("rejects a view declared as a table", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `CREATE VIEW app.catalog_view AS SELECT id FROM app.model_source`); err != nil {
			t.Fatal(err)
		}
		applySQL(t, pool, `SELECT id FROM app.catalog_view`, "mart.wrong_kind", contract(map[string]config.Column{
			"id": {Type: "integer"},
		}, nil, nil), "declare model/app.catalog_view", "table/app.catalog_view")
	})

	t.Run("accepts extra output columns", func(t *testing.T) {
		applySQL(t, pool, `SELECT id, label::varchar AS label, 42 AS spare FROM app.model_source`, "mart.extra", contract(map[string]config.Column{
			"id": {Type: "integer"}, "label": {Type: "text"},
		}, nil, nil), "", "table/app.model_source")
	})

	t.Run("rejects an extra SQL statement", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `CREATE TABLE app.statement_sentinel (id integer)`); err != nil {
			t.Fatal(err)
		}
		err := applySQL(t, pool, `SELECT id FROM app.model_source; DROP TABLE app.statement_sentinel`, "mart.injection", contract(map[string]config.Column{
			"id": {Type: "integer"},
		}, nil, nil), "postgres 42601", "table/app.model_source")
		if strings.Contains(err.Error(), "DROP TABLE") || strings.Contains(err.Error(), "SELECT id") {
			t.Fatalf("error exposed SQL: %v", err)
		}
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('app.statement_sentinel') IS NOT NULL`).Scan(&exists); err != nil || !exists {
			t.Fatalf("extra statement dropped sentinel table: exists=%v err=%v", exists, err)
		}
	})

	t.Run("contract failure preserves old view", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `CREATE OR REPLACE VIEW mart.preserved AS SELECT 7::integer AS id, 'old'::text AS label`); err != nil {
			t.Fatal(err)
		}
		applySQL(t, pool, `SELECT id, NULL::text AS label FROM app.model_source`, "mart.preserved", contract(map[string]config.Column{
			"id": {Type: "integer"}, "label": {Type: "text"},
		}, nil, nil), `column "label" contains NULL`, "table/app.model_source")
		var id int
		var label string
		if err := pool.QueryRow(ctx, `SELECT id, label FROM mart.preserved`).Scan(&id, &label); err != nil || id != 7 || label != "old" {
			t.Fatalf("last usable view was not preserved: id=%d label=%q err=%v", id, label, err)
		}
	})

	t.Run("rejects a missing required column", func(t *testing.T) {
		applySQL(t, pool, `SELECT id FROM app.model_source`, "mart.missing", contract(map[string]config.Column{
			"unknown": {Type: "integer"},
		}, nil, nil), `missing output column "unknown"`, "table/app.model_source")
	})

	t.Run("rejects null required values", func(t *testing.T) {
		applySQL(t, pool, `SELECT NULL::integer AS id`, "mart.nulls", contract(map[string]config.Column{
			"id": {Type: "integer"},
		}, nil, nil), `column "id" contains NULL`)
	})

	t.Run("rejects duplicate primary key values", func(t *testing.T) {
		applySQL(t, pool, `SELECT 1::integer AS id FROM generate_series(1, 2)`, "mart.duplicate_pk", contract(map[string]config.Column{
			"id": {Type: "integer"},
		}, []string{"id"}, nil), "primary key contains duplicate values")
	})

	t.Run("rejects duplicate unique key values", func(t *testing.T) {
		applySQL(t, pool, `SELECT 1::integer AS id FROM generate_series(1, 2)`, "mart.duplicate_unique", contract(map[string]config.Column{
			"id": {Type: "integer"},
		}, nil, []string{"id"}), "unique key contains duplicate values")
	})

	t.Run("records failure after context cancellation", func(t *testing.T) {
		root := t.TempDir()
		writeSQL(t, root, "cancel.sql", `SELECT id FROM app.model_source`)
		cfg := &config.Config{Models: map[string]config.Model{"mart.cancelled": {
			File: "cancel.sql", Materialization: "view", Reads: []string{"table/app.model_source"},
			Contract: contract(map[string]config.Column{"id": {Type: "integer"}}, nil, nil),
		}}}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := Apply(cancelled, pool, cfg, root); err == nil {
			t.Fatal("apply unexpectedly succeeded")
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM ops.model_refreshes WHERE model_ref='model/mart.cancelled' ORDER BY started_at DESC LIMIT 1`).Scan(&status); err != nil || status != "failed" {
			t.Fatalf("cancelled apply history: status=%q err=%v", status, err)
		}
	})

	t.Run("does not expose source values in SQL errors", func(t *testing.T) {
		err := applySQL(t, pool, `SELECT label::integer AS id FROM app.model_source`, "mart.safe_error", contract(map[string]config.Column{
			"id": {Type: "integer"},
		}, nil, nil), "postgres 22P02", "table/app.model_source")
		if strings.Contains(err.Error(), "one") {
			t.Fatalf("returned error exposed source data: %v", err)
		}
		var recorded string
		if err := pool.QueryRow(ctx, `SELECT error FROM ops.model_refreshes WHERE model_ref='model/mart.safe_error' ORDER BY started_at DESC LIMIT 1`).Scan(&recorded); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(recorded, "one") {
			t.Fatalf("history exposed source data: %q", recorded)
		}
	})
}

func applySQL(t *testing.T, pool *pgxpool.Pool, sql, name string, c config.Contract, wantError string, reads ...string) error {
	t.Helper()
	root := t.TempDir()
	writeSQL(t, root, "model.sql", sql)
	cfg := &config.Config{Models: map[string]config.Model{name: {
		File: "model.sql", Materialization: "view", Reads: reads, Contract: c,
	}}}
	_, err := Apply(context.Background(), pool, cfg, root)
	if wantError == "" && err != nil {
		t.Fatal(err)
	}
	if wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError)) {
		t.Fatalf("apply error = %v, want %q", err, wantError)
	}
	return err
}

func contract(columns map[string]config.Column, primary, unique []string) config.Contract {
	return config.Contract{Columns: columns, PrimaryKey: primary, UniqueKey: unique}
}

func assertPrivilege(t *testing.T, pool *pgxpool.Pool, role, relation string, want bool) {
	t.Helper()
	var got bool
	if err := pool.QueryRow(context.Background(), `SELECT has_table_privilege($1, $2, 'SELECT')`, role, relation).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s SELECT on %s: got %v, want %v", role, relation, got, want)
	}
}

func writeSQL(t *testing.T, root, name, sql string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ddp_models_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	poolConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		_ = admin.Close(ctx)
	})
	return pool
}
