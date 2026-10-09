package inspect

import (
	"reflect"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/registry"
)

func TestResourceLineageDataflowIsDeterministicAndExcludesOperationalEdges(t *testing.T) {
	reg := &registry.Registry{Relationships: []config.Relationship{
		{From: "job/sync", To: "integration/erp", Kind: "reads"},
		{From: "job/sync", To: "table/raw", Kind: "writes"},
		{From: "job/stage", To: "table/raw", Kind: "reads"},
		{From: "job/stage", To: "model/staging", Kind: "model"},
		{From: "model/core", To: "model/staging", Kind: "reads"},
		{From: "model/mart", To: "model/core", Kind: "reads"},
		{From: "endpoint/customers", To: "model/mart", Kind: "reads"},
		{From: "page/customers", To: "endpoint/customers", Kind: "endpoint"},
		{From: "health/raw_fresh", To: "table/raw", Kind: "target"},
		{From: "permission/admin", To: "endpoint/customers", Kind: "permission"},
		{From: "health/raw_fresh", To: "group/ops", Kind: "notify"},
		{From: "job/stage", To: "job/sync", Kind: "after"},
		{From: "job/loop", To: "table/loop", Kind: "writes"},
		{From: "job/loop", To: "table/loop", Kind: "reads"},
	}}

	tests := []struct {
		ref                  string
		upstream, downstream []string
	}{
		{"job/sync", []string{"integration/erp"}, []string{"endpoint/customers", "health/raw_fresh", "job/stage", "model/core", "model/mart", "model/staging", "page/customers", "table/raw"}},
		{"table/raw", []string{"integration/erp", "job/sync"}, []string{"endpoint/customers", "health/raw_fresh", "job/stage", "model/core", "model/mart", "model/staging", "page/customers"}},
		{"page/customers", []string{"endpoint/customers", "integration/erp", "job/stage", "job/sync", "model/core", "model/mart", "model/staging", "table/raw"}, []string{}},
		{"health/raw_fresh", []string{"integration/erp", "job/sync", "table/raw"}, []string{}},
		{"job/loop", []string{"table/loop"}, []string{"table/loop"}},
	}
	for _, test := range tests {
		got := ResourceLineage(reg, test.ref)
		if !reflect.DeepEqual(got.Upstream, test.upstream) || !reflect.DeepEqual(got.Downstream, test.downstream) {
			t.Errorf("ResourceLineage(%q) = %#v, want upstream=%#v downstream=%#v", test.ref, got, test.upstream, test.downstream)
		}
	}
}

func TestResourceLineageUsesValidatedRegistryRelationships(t *testing.T) {
	cfg, err := config.Load("../testdata/reporting/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Comms.Groups = map[string]config.Group{"ops": {Recipients: []string{"ops@example.test"}}}
	cfg.Health = map[string]config.Health{"customers_fresh": {
		Kind: "freshness", Target: "table/synthetic.customers", Column: "_loaded_at", MaxAge: "1h", Severity: "critical", Notify: []string{"group/ops"},
	}}
	reg, err := registry.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if got := ResourceLineage(reg, "page/customers").Upstream; !reflect.DeepEqual(got, []string{
		"endpoint/customers", "integration/synthetic", "job/refresh_customers", "job/sync_customers",
		"model/core.customers", "model/mart.customers", "model/staging.customers", "table/synthetic.customers",
	}) {
		t.Fatalf("page upstream = %#v", got)
	}
	if got := ResourceLineage(reg, "table/synthetic.customers").Downstream; !reflect.DeepEqual(got, []string{
		"endpoint/customers", "health/customers_fresh", "model/core.customers",
		"model/mart.customers", "model/staging.customers", "page/customers",
	}) {
		t.Fatalf("table downstream = %#v", got)
	}
	if got := ResourceLineage(reg, "job/sync_customers").Downstream; !reflect.DeepEqual(got, []string{
		"endpoint/customers", "health/customers_fresh", "job/refresh_customers", "model/core.customers",
		"model/mart.customers", "model/staging.customers", "page/customers", "table/synthetic.customers",
	}) {
		t.Fatalf("job downstream = %#v", got)
	}
}
