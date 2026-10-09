package config

import (
	"slices"
	"strings"
	"testing"
)

func TestTypedRelationships(t *testing.T) {
	fixture := func(t *testing.T) *Config {
		t.Helper()
		cfg, err := Load("../testdata/reporting/ddp.yaml")
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	cfg := fixture(t)
	links := cfg.Relationships()
	for _, want := range []Relationship{
		{From: "job/sync_customers", To: "integration/synthetic", Kind: "reads"},
		{From: "job/sync_customers", To: "table/synthetic.customers", Kind: "writes"},
		{From: "endpoint/customers", To: "permission/customers.read", Kind: "permission"},
		{From: "page/customers", To: "permission/customers.read", Kind: "permission"},
	} {
		if !slices.Contains(links, want) {
			t.Fatalf("missing relationship %+v", want)
		}
	}
	if !slices.Contains(cfg.References()["page/customers"], "permission/customers.read") {
		t.Fatal("permission relationship absent from validation graph")
	}
	for _, tc := range []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"unknown permission", func(c *Config) { delete(c.Permissions, "customers.read") }, "references unknown"},
		{"system page client permission", func(c *Config) {
			c.Pages["system"] = Page{Label: "System", Path: "/system", Kind: "system", Permission: "customers.read"}
		}, "system page requires admin policy"},
		{"system page missing policy", func(c *Config) {
			c.Pages["system"] = Page{Label: "System", Path: "/system", Kind: "system"}
		}, "system page requires admin policy"},
		{"wrong read kind", func(c *Config) {
			j := c.Jobs["sync_customers"]
			j.Reads = []string{"permission/customers.read"}
			c.Jobs["sync_customers"] = j
		}, "reads must reference"},
		{"wrong endpoint kind", func(c *Config) {
			p := c.Pages["customers"]
			p.Endpoint = "model/mart.customers"
			c.Pages["customers"] = p
		}, "endpoint must reference"},
		{"unknown policy", func(c *Config) {
			e := c.Endpoints["customers"]
			e.Policy = "role:admin"
			c.Endpoints["customers"] = e
		}, "requires public"},
		{"duplicate relation owner", func(c *Config) {
			c.Tables["mart.customers"] = c.Tables["synthetic.customers"]
		}, "same relation"},
		{"wrong notify kind", func(c *Config) {
			c.Health["customers"] = Health{Kind: "freshness", Target: "table/synthetic.customers", Notify: []string{"page/customers"}}
		}, "notify must reference groups"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixture(t)
			tc.edit(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
