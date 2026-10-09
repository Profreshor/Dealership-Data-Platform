package inspect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestResourcesCatalogAndReadonly(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	db := fmt.Sprintf("ddp_resources_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)")
	pc, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = db
	owner, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	c, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, c.Conn()); err != nil {
		c.Release()
		t.Fatal(err)
	}
	c.Release()
	_, err = owner.Exec(ctx, `CREATE SCHEMA erp_raw; CREATE TABLE erp_raw.items (id integer NOT NULL, payload text, loaded_at timestamptz); INSERT INTO erp_raw.items VALUES (1,'secret-row',now()); CREATE VIEW core.items AS SELECT id,payload FROM erp_raw.items; CREATE MATERIALIZED VIEW mart.summary AS SELECT count(*) AS total FROM core.items; GRANT USAGE ON SCHEMA erp_raw,staging,core,mart TO ddp_readonly; GRANT SELECT ON erp_raw.items,core.items,mart.summary TO ddp_readonly; INSERT INTO ops.executions(id,job_ref,scheduled_at,finished_at,status) VALUES ('01J00000000000000000000001','job/load',now(),now(),'succeeded'),('01J00000000000000000000002','job/load',now(),now()-interval '1 day','succeeded'); INSERT INTO ops.model_refreshes(id,model_ref,started_at,finished_at,status) SELECT 'refresh-'||g,'model/mart.summary',now()-(g||' hours')::interval,now()-(g||' hours')::interval,'succeeded' FROM generate_series(1,12) g; INSERT INTO ops.heartbeats(service_ref,instance_id,seen_at,state) VALUES ('service/scheduler','instance',now()-interval '10 seconds','running')`)
	if err != nil {
		t.Fatal(err)
	}
	readCfg := *pc
	readCfg.MaxConns, readCfg.MinConns = 2, 1
	readCfg.ConnConfig = pc.ConnConfig.Copy()
	readCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe
	readCfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, e := c.Exec(ctx, "SET ROLE ddp_readonly"); return e }
	readPool, err := pgxpool.NewWithConfig(ctx, &readCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer readPool.Close()
	cfg := &config.Config{Ddp: config.Identity{Name: "test", DisplayName: "Test", Timezone: "UTC"}, Serving: config.Serving{SessionTTL: "1h"}, Database: config.Database{Layers: []string{"staging", "core", "mart"}}, Scheduler: config.Scheduler{MaxWorkers: 1, Defaults: config.Defaults{Timeout: "1s", Retry: config.Retry{MaxAttempts: 1, InitialDelay: "1s", MaxDelay: "1s"}, Catchup: "latest_only"}}, Integrations: map[string]config.Integration{"erp": {Kind: "http", Auth: &config.IntegrationAuth{Type: "bearer", Secret: "ERP_TOKEN"}}}, Jobs: map[string]config.Job{"load": {Purpose: "load", Action: "ingest", Python: "jobs.load", Deletions: "ignore", Idempotency: &config.Idempotency{Strategy: "natural_key"}, Reads: []string{"integration/erp"}, Writes: []config.Write{{Target: "table/erp_raw.items"}}}}, Tables: map[string]config.Table{"core.items": {Purpose: "view", Contract: config.Contract{Columns: map[string]config.Column{"id": {Type: "integer", Nullable: false}, "payload": {Type: "text", Nullable: true}}}}, "erp_raw.items": {Purpose: "items", Contract: config.Contract{Columns: map[string]config.Column{"id": {Type: "integer", Nullable: false}, "payload": {Type: "text", Nullable: true}}}}}, Models: map[string]config.Model{"mart.summary": {File: "models/mart/summary.sql", Purpose: "summary", Materialization: "materialized_view", Reads: []string{"table/core.items"}, Contract: config.Contract{Columns: map[string]config.Column{"total": {Type: "bigint", Nullable: false}}, UniqueKey: []string{"total"}}}}, Health: map[string]config.Health{"items": {Kind: "freshness", Target: "table/erp_raw.items", Column: "loaded_at", MaxAge: "1h", Severity: "warning"}}}
	cfg.Tables["erp_raw.items"].Contract.Columns["loaded_at"] = config.Column{Type: "timestamptz"}
	t.Setenv("ERP_TOKEN", "synthetic-secret-value-must-not-appear")
	cfg.Tables["erp_raw.missing"] = cfg.Tables["erp_raw.items"]
	listed, err := ListTables(ctx, readPool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	foundSource, foundMissing := false, false
	for _, table := range listed {
		if table.Ref == "table/erp_raw.items" {
			foundSource = table.Exists
		}
		if table.Ref == "table/erp_raw.missing" {
			foundMissing = !table.Exists
		}
	}
	if !foundSource || !foundMissing {
		t.Fatalf("source/missing declaration not listed: %+v", listed)
	}
	ints, err := ListIntegrations(ctx, readPool, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ints) != 1 || len(ints[0].SecretNames) != 1 || ints[0].SecretNames[0] != "ERP_TOKEN" || strings.Contains(fmt.Sprint(ints), "secret-row") {
		t.Fatalf("integration output leaked or missing: %+v", ints)
	}
	r, err := ShowTable(ctx, readPool, cfg, "model/mart.summary")
	if err != nil || !r.Exists || r.Kind != "materialized_view" || len(r.Columns) != 1 || r.Columns[0].Name != "total" {
		t.Fatalf("materialized catalog: %+v %v", r, err)
	}
	core, err := ShowTable(ctx, readPool, cfg, "table/core.items")
	if err != nil || len(core.DirectDependencies) != 1 || core.DirectDependencies[0] != "table/erp_raw.items" {
		t.Fatalf("view dependencies: %+v %v", core, err)
	}
	if len(r.DirectDependencies) != 1 || r.DirectDependencies[0] != "table/core.items" {
		t.Fatalf("materialized dependencies: %+v", r)
	}
	missing, err := ShowTable(ctx, readPool, cfg, "table/core.missing")
	if err != nil || missing.Exists {
		t.Fatalf("missing relation: %+v %v", missing, err)
	}
	d, err := Inspect(ctx, readPool, cfg, "model/mart.summary")
	if err != nil || d.Model == nil || len(d.Model.Refreshes) != 10 || d.Model.Refreshes[0].Status != "succeeded" || len(d.Health) != 0 {
		t.Fatalf("model detail: %+v %v", d, err)
	}
	integration, err := ShowIntegration(ctx, readPool, cfg, "integration/erp")
	if err != nil || len(integration.Jobs) != 1 || len(integration.LandedTables) != 1 || integration.LandedTables[0] != "table/erp_raw.items" || len(integration.Relations) != 1 || integration.Relations[0].LastSuccessfulJobFinishedAt == nil {
		t.Fatalf("integration edges/freshness: %+v %v", integration, err)
	}
	serialized, err := json.Marshal(integration)
	if err != nil || strings.Contains(string(serialized), os.Getenv("ERP_TOKEN")) || strings.Contains(string(serialized), "secret-row") || !strings.Contains(string(serialized), "ERP_TOKEN") {
		t.Fatalf("inspection leaked private values or hid secret name: %s %v", serialized, err)
	}
	if !integration.Relations[0].Exists || len(integration.Relations[0].Columns) != 3 || integration.Relations[0].Columns[0].Type != "integer" || integration.Relations[0].Columns[0].Nullable || !integration.Relations[0].Columns[1].Nullable {
		t.Fatalf("source column metadata: %+v", integration.Relations)
	}
	if !slices.IsSortedFunc(d.Model.Refreshes, func(a, b ModelRefresh) int { return b.StartedAt.Compare(a.StartedAt) }) || time.Since(d.Model.Refreshes[0].StartedAt) > 2*time.Hour || time.Since(d.Model.Refreshes[9].StartedAt) > 11*time.Hour {
		t.Fatalf("refresh history not newest ten: %+v", d.Model.Refreshes)
	}
	job, err := Inspect(ctx, readPool, cfg, "job/load")
	if err != nil || job.Job == nil {
		t.Fatalf("job observation: %+v %v", job, err)
	}
	execution, err := Inspect(ctx, readPool, cfg, "execution/01J00000000000000000000001")
	if err != nil || execution.Execution == nil {
		t.Fatalf("execution observation: %+v %v", execution, err)
	}
	s, err := Inspect(ctx, readPool, cfg, "service/scheduler")
	if err != nil || s.Scheduler == nil || s.Scheduler.State != "stale" {
		t.Fatalf("scheduler status: %+v %v", s, err)
	}
	if _, err := owner.Exec(ctx, "REVOKE SELECT ON ops.executions FROM ddp_readonly"); err != nil {
		t.Fatal(err)
	}
	if _, err := ShowIntegration(ctx, readPool, cfg, "integration/erp"); err == nil {
		t.Fatal("integration inspection ignored refused ops history query")
	}
}
