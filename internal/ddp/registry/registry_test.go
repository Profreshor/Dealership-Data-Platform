package registry

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestBuildFixtureGraph(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "testdata", "reporting", "ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated builds differ")
	}
	if len(first.Types) != 9 || len(first.Resources) != 11 {
		t.Fatalf("unexpected registry sizes: types=%d resources=%d", len(first.Types), len(first.Resources))
	}
	for _, resource := range first.Resources {
		if resource.Dependencies == nil || resource.Dependents == nil || resource.ReadBy == nil || resource.WrittenBy == nil {
			t.Fatalf("resource %s has nil relationship list", resource.Ref)
		}
	}

	job, err := first.Get("job/sync_customers")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(job.Dependencies, []string{"integration/synthetic", "table/synthetic.customers"}) {
		t.Fatalf("job dependencies: %#v", job.Dependencies)
	}
	table, err := first.Get("table/synthetic.customers")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(table.ReadBy, []string{"model/staging.customers"}) ||
		!reflect.DeepEqual(table.WrittenBy, []string{"job/sync_customers"}) {
		t.Fatalf("table reverse edges: read_by=%#v written_by=%#v", table.ReadBy, table.WrittenBy)
	}
	page, err := first.Get("page/customers")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page.Dependencies, []string{"endpoint/customers", "permission/customers.read"}) {
		t.Fatalf("page dependencies: %#v", page.Dependencies)
	}
	permission, err := first.Get("permission/customers.read")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(permission.Dependents, []string{"endpoint/customers", "page/customers"}) {
		t.Fatalf("permission dependents: %#v", permission.Dependents)
	}
	if _, err := first.Get("model/missing"); err == nil {
		t.Fatal("unknown Get unexpectedly succeeded")
	}
	if got := first.Search("REPORTING ROWS"); len(got) != 1 || got[0].Ref != "model/mart.customers" {
		t.Fatalf("purpose search: %#v", got)
	}
	if got := first.Search("SYNTHETIC_API_KEY"); len(got) != 1 || got[0].Ref != "integration/synthetic" {
		t.Fatalf("config search: %#v", got)
	}

	// A reconciliation job can both read and write the same table. Keep both
	// relationship kinds while listing the referenced table only once.
	declaration := cfg.Jobs["sync_customers"]
	declaration.Reads = append(declaration.Reads, "table/synthetic.customers")
	cfg.Jobs["sync_customers"] = declaration
	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range graph.Resources {
		got, err := graph.Get(resource.Ref)
		if err != nil || !reflect.DeepEqual(got, resource) {
			t.Fatalf("Get/list differ for %s: %+v, %+v (%v)", resource.Ref, got, resource, err)
		}
	}
	after, err := json.Marshal(cfg)
	if err != nil || string(before) != string(after) {
		t.Fatalf("Build changed config: %v", err)
	}
}

func TestBuildNilConfig(t *testing.T) {
	if _, err := Build(nil); err == nil {
		t.Fatal("nil config unexpectedly succeeded")
	}
	if _, err := (*Registry)(nil).Get("model/missing"); err == nil {
		t.Fatal("nil registry Get unexpectedly succeeded")
	}
}

func TestBuildEmptyConfigTypesAndLists(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "testdata", "base", "ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The template includes the operator console; this fixture exercises no resources.
	cfg.Pages = map[string]config.Page{}
	got, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Resources) != 0 || len(got.Relationships) != 0 || len(got.Types) != 9 {
		t.Fatalf("empty registry: %#v", got)
	}
}
