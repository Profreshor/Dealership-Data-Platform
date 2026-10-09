package models

import (
	"context"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestPlanIsOrderedReadOnlyAndShowsSQL(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE app.plan_source (id integer); CREATE VIEW staging.plan_base AS SELECT id FROM app.plan_source`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeSQL(t, root, "staging.sql", "SELECT id FROM app.plan_source")
	writeSQL(t, root, "core.sql", "SELECT id FROM staging.plan_base")
	writeSQL(t, root, "mart.sql", "SELECT id FROM core.plan_middle")
	cfg := &config.Config{Models: map[string]config.Model{
		"mart.plan_final":   {File: "mart.sql", Materialization: "view", Reads: []string{"model/core.plan_middle"}},
		"core.plan_middle":  {File: "core.sql", Materialization: "view", Reads: []string{"model/staging.plan_base"}},
		"staging.plan_base": {File: "staging.sql", Materialization: "view", Reads: []string{"table/app.plan_source"}},
	}}
	var before int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ops.model_refreshes").Scan(&before); err != nil {
		t.Fatal(err)
	}
	steps, err := Plan(ctx, pool, cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 || steps[0].ModelRef != "model/staging.plan_base" || steps[1].ModelRef != "model/core.plan_middle" || steps[2].ModelRef != "model/mart.plan_final" {
		t.Fatalf("unexpected plan order: %#v", steps)
	}
	if steps[0].CurrentMaterialization != "view" || steps[0].CurrentSQL == nil || !strings.Contains(*steps[0].CurrentSQL, "plan_source") {
		t.Fatalf("current relation not shown: %#v", steps[0])
	}
	if steps[0].ProposedSQL != "SELECT id FROM app.plan_source" || steps[1].ProposedSQL != "SELECT id FROM staging.plan_base" {
		t.Fatalf("proposed SQL not shown: %#v", steps)
	}
	if steps[1].CurrentMaterialization != "" || steps[1].CurrentSQL != nil || steps[2].CurrentMaterialization != "" || steps[2].CurrentSQL != nil {
		t.Fatalf("empty current relations not represented: %#v", steps)
	}
	var after int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ops.model_refreshes").Scan(&after); err != nil || after != before {
		t.Fatalf("plan wrote refresh history: before=%d after=%d err=%v", before, after, err)
	}
	for _, relation := range []string{"staging.plan_base", "core.plan_middle", "mart.plan_final"} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", relation).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if (relation == "staging.plan_base") != exists {
			t.Fatalf("plan mutated relation %s: exists=%v", relation, exists)
		}
	}
}
