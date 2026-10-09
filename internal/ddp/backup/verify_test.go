package backup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVerifyRestoreRequiresRestoredDatabaseAndRegistry(t *testing.T) {
	if err := verifyRestore(context.Background(), nil, &config.Config{}); err == nil || !strings.Contains(err.Error(), "pool and config are required") {
		t.Fatalf("nil pool error = %v", err)
	}
	if err := verifyRestore(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "pool and config are required") {
		t.Fatalf("nil config error = %v", err)
	}
}

func TestVerifyRestoreRealPostgresIsReadOnly(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	adminCfg, err := pgx.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	database := fmt.Sprintf("ddp_restore_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	dbCfg := adminCfg.Copy()
	dbCfg.Database = database
	owner, err := pgx.ConnectConfig(ctx, dbCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner); err != nil {
		owner.Close(ctx)
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `CREATE TABLE core.restore_probe(id integer PRIMARY KEY);
INSERT INTO core.restore_probe VALUES (1);
GRANT SELECT ON core.restore_probe TO ddp_api;
CREATE FUNCTION core.restore_bump() RETURNS integer LANGUAGE plpgsql AS $$ BEGIN UPDATE core.restore_probe SET id=id+1; RETURN 1; END $$;
CREATE VIEW core.restore_probe_view AS SELECT id FROM core.restore_probe;
GRANT SELECT ON core.restore_probe_view TO ddp_api`); err != nil {
		owner.Close(ctx)
		t.Fatal(err)
	}
	var ownerRelation string
	if err := owner.QueryRow(ctx, "SELECT to_regclass('core.restore_probe')::text").Scan(&ownerRelation); err != nil || ownerRelation == "" {
		owner.Close(ctx)
		t.Fatalf("owner cannot see restored relation: %q %v", ownerRelation, err)
	}
	owner.Close(ctx)
	apiCfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	apiCfg.ConnConfig.Database = database
	apiCfg.ConnConfig.RuntimeParams["role"] = "ddp_api"
	api, err := pgxpool.NewWithConfig(ctx, apiCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	var currentDatabase, user string
	var relation *string
	if err := api.QueryRow(ctx, "SELECT current_database(), current_user, to_regclass('core.restore_probe')::text").Scan(&currentDatabase, &user, &relation); err != nil || relation == nil {
		t.Fatalf("API cannot see restored relation in %s as %s: %v", currentDatabase, user, err)
	}
	cfg := &config.Config{
		Tables:    map[string]config.Table{"core.restore_probe": {Contract: config.Contract{Columns: map[string]config.Column{"id": {Type: "integer"}}, PrimaryKey: []string{"id"}}}},
		Endpoints: map[string]config.Endpoint{"probe": {Reads: []string{"table/core.restore_probe"}, Columns: []string{"id"}, UniqueKey: []string{"id"}, Path: "/api/probe"}},
	}
	snapshot := func() string {
		var value string
		if err := api.QueryRow(ctx, `SELECT (SELECT count(*) FROM ops.executions)::text || ':' || (SELECT count(*) FROM ops.outbox)::text || ':' || (SELECT count(*) FROM ddp.audit)::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	if err := verifyRestore(ctx, api, cfg); err != nil {
		t.Fatal(err)
	}
	if err := execAdminRestore(ctx, dbCfg, "INSERT INTO core.restore_probe VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestore(ctx, api, cfg); err != nil {
		t.Fatalf("two-row list endpoint: %v", err)
	}
	if err := execAdminRestore(ctx, dbCfg, "CREATE TABLE core.private_landing(id integer PRIMARY KEY); INSERT INTO core.private_landing VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	cfg.Tables["core.private_landing"] = cfg.Tables["core.restore_probe"]
	if err := verifyRestore(ctx, api, cfg); err != nil {
		t.Fatalf("landed contract incorrectly needs API access: %v", err)
	}
	cfg.Endpoints["probe"] = config.Endpoint{Reads: []string{"table/core.restore_probe"}, Columns: []string{"id"}, Shape: "singleton", Path: "/api/probe"}
	if err := verifyRestore(ctx, api, cfg); err == nil || !strings.Contains(err.Error(), "more than one row") {
		t.Fatalf("two-row singleton endpoint error = %v", err)
	}
	cfg.Endpoints["probe"] = config.Endpoint{Reads: []string{"table/core.restore_probe"}, Columns: []string{"id"}, UniqueKey: []string{"id"}, Path: "/api/probe"}
	if after := snapshot(); after != before {
		t.Fatalf("restore read-only smoke changed operational rows: %s -> %s", before, after)
	}
	adminConn, err := pgx.ConnectConfig(ctx, dbCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminConn.Exec(ctx, "REVOKE SELECT ON core.restore_probe FROM ddp_api; REVOKE SELECT ON core.restore_probe_view FROM ddp_api"); err != nil {
		adminConn.Close(ctx)
		t.Fatal(err)
	}
	cfg.Tables["core.restore_probe_view"] = cfg.Tables["core.restore_probe"]
	cfg.Endpoints["probe"] = config.Endpoint{Reads: []string{"table/core.restore_probe_view"}, Columns: []string{"id"}, Path: "/api/probe"}
	if err := verifyRestore(ctx, api, cfg); err == nil {
		t.Fatal("missing API SELECT grant passed restore read-only smoke")
	}
	if _, err := adminConn.Exec(ctx, "GRANT SELECT ON core.restore_probe, core.restore_probe_view TO ddp_api; CREATE OR REPLACE VIEW core.restore_probe_view AS SELECT core.restore_bump() AS id"); err != nil {
		adminConn.Close(ctx)
		t.Fatal(err)
	}
	adminConn.Close(ctx)
	if err := verifyRestore(ctx, api, cfg); err == nil || !strings.Contains(err.Error(), "postgres 25006") {
		t.Fatalf("writable function was not blocked by read-only restore smoke: %v", err)
	}
}

func execAdminRestore(ctx context.Context, cfg *pgx.ConnConfig, statement string) error {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, statement)
	conn.Close(ctx)
	return err
}
