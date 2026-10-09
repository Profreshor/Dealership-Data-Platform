package models

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestModelAuditsAreBoundedAndTransactional(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE app.audit_source (id integer); INSERT INTO app.audit_source VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeSQL(t, root, "model.sql", `SELECT id FROM app.audit_source`)
	cfg := &config.Config{Models: map[string]config.Model{
		"mart.audit_model": {File: "model.sql", Materialization: "view", Reads: []string{"table/app.audit_source"}, Contract: contract(map[string]config.Column{"id": {Type: "integer"}}, nil, nil)},
	}}
	if _, err := Apply(ctx, pool, cfg, root); err != nil {
		t.Fatal(err)
	}
	var action, target, principal, outcome string
	if err := pool.QueryRow(ctx, `SELECT action,target,principal,outcome::text FROM ddp.audit WHERE action='models.apply'`).Scan(&action, &target, &principal, &outcome); err != nil {
		t.Fatal(err)
	}
	if action != "models.apply" || target != "model/mart.audit_model" || principal == "" {
		t.Fatalf("unexpected audit identity: %s %s %s", action, target, principal)
	}
	var facts map[string]string
	if err := json.Unmarshal([]byte(outcome), &facts); err != nil || facts["status"] != "succeeded" || facts["refresh_id"] == "" || len(facts) != 2 {
		t.Fatalf("unexpected bounded outcome: %s", outcome)
	}
	before := modelScalar[int](t, pool, `SELECT count(*) FROM ddp.audit`)
	if err := Verify(ctx, pool, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Plan(ctx, pool, cfg, root); err != nil {
		t.Fatal(err)
	}
	if after := modelScalar[int](t, pool, `SELECT count(*) FROM ddp.audit`); after != before {
		t.Fatalf("read-only model commands wrote audit: %d -> %d", before, after)
	}

	if _, err := pool.Exec(ctx, `CREATE FUNCTION ddp.reject_model_success_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='models.refresh' AND NEW.outcome->>'status'='succeeded' THEN RAISE EXCEPTION 'blocked'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_model_success_audit BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION ddp.reject_model_success_audit()`); err != nil {
		t.Fatal(err)
	}
	writeSQL(t, root, "model.sql", `SELECT id + 1 AS id FROM app.audit_source`)
	if _, err := Refresh(ctx, pool, cfg, root, "model/mart.audit_model"); err == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}
	if got := modelScalar[int](t, pool, `SELECT id FROM mart.audit_model`); got != 1 {
		t.Fatalf("successful audit failure committed relation: %d", got)
	}
	if got := modelScalar[int](t, pool, `SELECT count(*) FROM ops.model_refreshes`); got != 2 {
		t.Fatalf("successful audit failure did not preserve failure evidence: %d", got)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_model_success_audit ON ddp.audit; DROP FUNCTION ddp.reject_model_success_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION ddp.reject_model_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='models.refresh' THEN RAISE EXCEPTION 'blocked'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_model_audit BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION ddp.reject_model_audit()`); err != nil {
		t.Fatal(err)
	}
	writeSQL(t, root, "model.sql", `SELECT missing FROM app.audit_source`)
	if _, err := Refresh(ctx, pool, cfg, root, "model/mart.audit_model"); err == nil {
		t.Fatal("invalid refresh unexpectedly succeeded")
	}
	if got := modelScalar[int](t, pool, `SELECT count(*) FROM ops.model_refreshes`); got != 2 {
		t.Fatalf("failed audit left failure evidence: %d", got)
	}
	if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION ddp.reject_model_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='models.refresh' THEN IF NEW.outcome->>'status'='succeeded' THEN RAISE EXCEPTION USING ERRCODE='42501', MESSAGE='private permission detail'; END IF; RAISE EXCEPTION 'private recording detail'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	writeSQL(t, root, "model.sql", `SELECT id + 1 AS id FROM app.audit_source`)
	if _, err := Refresh(ctx, pool, cfg, root, "model/mart.audit_model"); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("failure recording lost original refusal: %v", err)
	}
	if got := modelScalar[int](t, pool, `SELECT count(*) FROM ops.model_refreshes`); got != 2 {
		t.Fatalf("double audit failure left history: %d", got)
	}
}

func modelScalar[T any](t *testing.T, pool *pgxpool.Pool, query string) T {
	t.Helper()
	var value T
	if err := pool.QueryRow(context.Background(), query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
