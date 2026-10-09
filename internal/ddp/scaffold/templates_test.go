package scaffold

import (
	"os"
	"strings"
	"testing"
)

func baseRegistry(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuildJobPreservesRegistryAndEmitsSource(t *testing.T) {
	registry := append([]byte("# keep this comment\n"), baseRegistry(t)...)
	p, err := Build(registry, Request{Kind: "job", Name: "sync_customers", Definition: []byte("purpose: Sync customers\naction: check\n")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.Registry), "# keep this comment") || !strings.Contains(string(p.Registry), "python: jobs.sync_customers") {
		t.Fatalf("registry lost comment or generated python: %s", p.Registry)
	}
	if string(p.Files["jobs/sync_customers.py"]) == "" {
		t.Fatal("job source was not emitted")
	}
}

func TestBuildModelAndMigration(t *testing.T) {
	registry := []byte(strings.Replace(string(baseRegistry(t)), "tables: {}", "tables:\n  core.customers:\n    purpose: Customers\n    contract: {columns: {id: {type: text, nullable: false}}}", 1))
	p, err := Build(registry, Request{Kind: "model", Name: "mart.customers", Definition: []byte("purpose: Customers\nmaterialization: view\nreads: [table/core.customers]\ncontract: {columns: {id: {type: text, nullable: false}}}\n"), Source: []byte("SELECT id FROM core.customers")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Files["models/mart/customers.sql"]; !ok || !strings.Contains(string(p.Registry), "file: models/mart/customers.sql") {
		t.Fatalf("model proposal incomplete: %#v", p)
	}
	m, err := Build([]byte("invalid: true\n"), Request{Kind: "migration", Name: "20260904120000_add_customers", Source: []byte("CREATE TABLE x (id text);")})
	if err == nil || m.Registry != nil {
		t.Fatal("invalid registry accepted for migration")
	}
	m, err = Build(baseRegistry(t), Request{Kind: "migration", Name: "20260904120000_add_customers", Source: []byte("CREATE TABLE x (id text);")})
	if err != nil || string(m.Files["migrations/app/20260904120000_add_customers.sql"]) == "" {
		t.Fatalf("migration proposal failed: %v", err)
	}
}

func TestBuildRejectsBadRequests(t *testing.T) {
	tests := []Request{
		{Kind: "job", Name: "bad-name", Definition: []byte("purpose: x\naction: check")},
		{Kind: "endpoint", Name: "customers", Definition: []byte("reads: [table/core.customers]\npath: /api/customers\npolicy: public\ncolumns: [id]\n")},
		{Kind: "route", Name: "customers"},
		{Kind: "model", Name: "mart.customers", Definition: []byte("purpose: x\nmaterialization: view\nreads: [table/core.customers]\ncontract: {columns: {id: {type: text, nullable: false}}}\n")},
	}
	for _, request := range tests {
		if _, err := Build(baseRegistry(t), request); err == nil {
			t.Fatalf("accepted invalid request %#v", request)
		}
	}
	first, err := Build(baseRegistry(t), Request{Kind: "job", Name: "sync_customers", Definition: []byte("purpose: x\naction: check")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(first.Registry, Request{Kind: "job", Name: "sync_customers", Definition: []byte("purpose: x\naction: check")}); err == nil {
		t.Fatal("duplicate was accepted")
	}
}

func TestBuildRejectsGeneratedFieldConflictsAndIgnoredInputs(t *testing.T) {
	for _, request := range []Request{
		{Kind: "job", Name: "sync_customers", Definition: []byte("purpose: x\naction: check\npython: jobs.other\n")},
		{Kind: "job", Name: "sync_customers", Definition: []byte("purpose: x\naction: check\npython: 3\n")},
		{Kind: "endpoint", Name: "customers", Definition: []byte("reads: [table/core.customers]\npath: /api/customers\npolicy: public\ncolumns: [id]\n"), Source: []byte("source")},
		{Kind: "page", Name: "customers", Definition: []byte("label: Customers\npath: /customers\nkind: table\nendpoint: endpoint/customers\n"), Source: []byte("source")},
		{Kind: "health", Name: "customers", Definition: []byte("kind: freshness\nseverity: warning\nnotify: [group/ops]\n"), Source: []byte("SELECT 1")},
		{Kind: "migration", Name: "20260904120000_add_customers", Definition: []byte("ignored: true\n"), Source: []byte("SELECT 1")},
	} {
		if _, err := Build(baseRegistry(t), request); err == nil {
			t.Fatalf("accepted conflicting/ignored input: %#v", request)
		}
	}
}

func TestBuildRejectsSQLHealthWithoutSource(t *testing.T) {
	_, err := Build(baseRegistry(t), Request{Kind: "health", Name: "customers", Definition: []byte("kind: sql\nseverity: warning\nnotify: [group/ops]\n")})
	if err == nil || !strings.Contains(err.Error(), "requires non-empty SQL source") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildRejectsExplicitBlankSource(t *testing.T) {
	for _, kind := range []string{"job", "integration", "health"} {
		for _, source := range [][]byte{{}, []byte(" \n\t")} {
			_, err := Build(baseRegistry(t), Request{Kind: kind, Name: "check_records", Definition: []byte("kind: sql\n"), Source: source})
			if err == nil || !strings.Contains(err.Error(), "source must not be empty") {
				t.Fatalf("%s accepted or masked blank source: %v", kind, err)
			}
		}
	}
}

func TestBuildAllSupportedArtifacts(t *testing.T) {
	registry := strings.Replace(string(baseRegistry(t)), "tables: {}", "tables:\n  core.customers:\n    purpose: Customers\n    contract: {columns: {id: {type: text, nullable: false}, loaded_at: {type: timestamp, nullable: false}}}", 1)
	registry = strings.Replace(registry, "comms: {}", "comms:\n  groups:\n    ops:\n      recipients: [ops@example.com]", 1)
	for _, request := range []Request{
		{Kind: "job", Name: "check_customers", Definition: []byte("purpose: Check\naction: check\n")},
		{Kind: "integration", Name: "erp", Definition: []byte("kind: http\n")},
	} {
		p, err := Build([]byte(registry), request)
		if err != nil {
			t.Fatalf("%s failed: %v", request.Kind, err)
		}
		registry = string(p.Registry)
	}
	endpoint, err := Build([]byte(registry), Request{Kind: "endpoint", Name: "customers", Definition: []byte("reads: [table/core.customers]\npath: /api/customers\npolicy: public\ncolumns: [id]\nshape: singleton\n")})
	if err != nil {
		t.Fatalf("endpoint failed: %v", err)
	}
	page, err := Build(endpoint.Registry, Request{Kind: "page", Name: "customers", Definition: []byte("label: Customers\npath: /customers\nkind: table\nendpoint: endpoint/customers\n")})
	if err != nil {
		t.Fatalf("page failed: %v", err)
	}
	if _, err := Build(page.Registry, Request{Kind: "health", Name: "customers", Definition: []byte("kind: freshness\ntarget: table/core.customers\ncolumn: loaded_at\nmax_age: 1h\nseverity: warning\nnotify: [group/ops]\n")}); err != nil {
		t.Fatalf("health failed: %v", err)
	}
}

func TestBuildRejectsAliasesMultipleDocumentsAndBadMigrationTime(t *testing.T) {
	for _, request := range []Request{
		{Kind: "integration", Name: "erp", Definition: []byte("kind: &kind http\nfoo: *kind\n")},
		{Kind: "integration", Name: "erp", Definition: []byte("kind: http\n---\nkind: http\n")},
		{Kind: "migration", Name: "20261301120000_bad_time", Source: []byte("SELECT 1")},
	} {
		if _, err := Build(baseRegistry(t), request); err == nil {
			t.Fatalf("accepted malformed request: %#v", request)
		}
	}
}
