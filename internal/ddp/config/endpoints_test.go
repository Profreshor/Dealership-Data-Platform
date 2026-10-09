package config

import (
	"os"
	"strings"
	"testing"
)

func TestEndpointDeclarationsConstrainQueries(t *testing.T) {
	raw, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tables["core.records"] = Table{Purpose: "Records", Contract: Contract{
		Columns: map[string]Column{"id": {Type: "text"}, "city": {Type: "text", Nullable: true}, "private": {Type: "text"}}, PrimaryKey: []string{"id"},
	}}
	base := Endpoint{Reads: []string{"table/core.records"}, Path: "/api/records", Policy: "public", Columns: []string{"id", "city"}, UniqueKey: []string{"id"},
		Sort: []string{"city"}, Filters: []string{"city"}, Search: []string{"city"}, Export: &Export{Format: "csv", MaxRows: 50000}}
	if err := cfg.ValidateEndpoint(base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Endpoint)
		want   string
	}{
		{"unknown column", func(e *Endpoint) { e.Columns = []string{"id", "missing"} }, "unknown column"},
		{"duplicate column", func(e *Endpoint) { e.Columns = []string{"id", "id"} }, "repeats column"},
		{"hidden filter", func(e *Endpoint) { e.Filters = []string{"private"} }, "must be returned"},
		{"unknown sort", func(e *Endpoint) { e.Sort = []string{"-missing"} }, "unknown column"},
		{"duplicate sort", func(e *Endpoint) { e.Sort = []string{"city", "-city"} }, "repeats column"},
		{"unproven identity", func(e *Endpoint) { e.UniqueKey = []string{"city"} }, "non-nullable"},
		{"absent identity", func(e *Endpoint) { e.UniqueKey = nil }, "needs unique_key"},
		{"wildcard route", func(e *Endpoint) { e.Path = "/api/{secret}" }, "literal path"},
		{"bad ceiling", func(e *Endpoint) { e.Export = &Export{Format: "csv", MaxRows: 0} }, "max_rows"},
		{"bad format", func(e *Endpoint) { e.Export = &Export{Format: "json", MaxRows: 10} }, "csv"},
		{"ambiguous singleton", func(e *Endpoint) { e.Shape = "singleton" }, "singleton cannot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := base
			tc.change(&e)
			if err := cfg.ValidateEndpoint(e); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("wanted %s, got %v", tc.want, err)
			}
		})
	}
	cfg.Tables["core.records"] = Table{Purpose: "Records", Contract: Contract{Columns: cfg.Tables["core.records"].Contract.Columns}}
	if err := cfg.ValidateEndpoint(base); err == nil || !strings.Contains(err.Error(), "relation contract") {
		t.Fatalf("unproven unique key accepted: %v", err)
	}
}
