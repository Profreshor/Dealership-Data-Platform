package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/registry"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/render"
)

func TestMachineContract(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", "")
	for _, tc := range []struct {
		args []string
		code int
		ok   bool
	}{
		{[]string{"version", "--json"}, 0, true},
		{[]string{"serve", "--json"}, 2, false},
		{[]string{"serve", "--all=false", "--json"}, 2, false},
		{[]string{"serve", "--all", "unexpected", "--json"}, 2, false},
		{[]string{"serve", "--help", "--json"}, 0, true},
		{[]string{"dev", "--comms", "--config", "does-not-exist", "--json"}, 2, false},
		{[]string{"models", "plan", "unexpected", "--json"}, 2, false},
		{[]string{"models", "verify", "unexpected", "--json"}, 2, false},
		{[]string{"models", "refresh", "--json"}, 2, false},
		{[]string{"models", "--help", "--json"}, 0, true},
		{[]string{"runs", "show", "--json"}, 2, false},
		{[]string{"runs", "list", "unexpected", "--json"}, 2, false},
		{[]string{"logs", "--json"}, 2, false},
		{[]string{"runs", "--help", "--json"}, 0, true},
		{[]string{"--json"}, 0, true},
		{[]string{"help", "--json"}, 0, true},
		{[]string{"config", "--help", "--json"}, 0, true},
		{[]string{"config", "diff", "one.yaml", "two.yaml", "--json"}, 2, false},
		{[]string{"config", "diff", "--help", "--json"}, 0, true},
		{[]string{"check", "unexpected", "--json"}, 2, false},
		{[]string{"check", "--help", "--json"}, 0, true},
		{[]string{"validate", "--config", "../testdata/base/ddp.yaml", "--json"}, 0, true},
		{[]string{"registry", "--config", "../testdata/base/ddp.yaml", "--json"}, 0, true},
		{[]string{"registry", "unexpected", "--json"}, 2, false},
		{[]string{"search", "--json"}, 2, false},
		{[]string{"search", " ", "--json"}, 2, false},
		{[]string{"inspect", "--json"}, 2, false},
		{[]string{"diagnose", "--json"}, 2, false},
		{[]string{"diagnose", "--help", "--json"}, 0, true},
		{[]string{"status", "unexpected", "--json"}, 2, false},
		{[]string{"status", "--help", "--json"}, 0, true},
		{[]string{"tables", "show", "--json"}, 2, false},
		{[]string{"integrations", "show", "--json"}, 2, false},
		{[]string{"new", "job", "--json"}, 2, false},
		{[]string{"new", "--help", "--json"}, 0, true},
		{[]string{"routes", "unexpected", "--json"}, 2, false},
		{[]string{"validate", "--config", "does-not-exist", "--json"}, 1, false},
		{[]string{"smoke", "--config", "../testdata/base/ddp.yaml", "--json"}, 1, false},
		{[]string{"smoke", "--help", "--json"}, 0, true},
		{[]string{"version", "unexpected", "--json"}, 2, false},
		{[]string{"version", "--unknown", "--json"}, 2, false},
	} {
		var out, errOut bytes.Buffer
		if got := Execute(context.Background(), tc.args, &out, &errOut, nil); got != tc.code {
			t.Fatalf("%v: code %d: %s %s", tc.args, got, &out, &errOut)
		}
		var envelope render.Envelope
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err, string(out.Bytes()))
		}
		if envelope.Version != 1 || envelope.OK != tc.ok || (envelope.Error == nil) != tc.ok {
			t.Fatalf("%v: %+v", tc.args, envelope)
		}
		if errOut.Len() != 0 {
			t.Fatal("machine error escaped to stderr", errOut.String())
		}
	}
}

func TestRoutesDescribeRegisteredPoliciesWithoutDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "not-a-database")
	var out, errOut bytes.Buffer
	if code := Execute(t.Context(), []string{"routes", "--config", "../testdata/reporting/ddp.yaml", "--json"}, &out, &errOut, nil); code != 0 {
		t.Fatalf("DB-free route listing: %d %s %s", code, out.String(), errOut.String())
	}
	var result struct {
		Data []struct {
			Pattern string `json:"pattern"`
			Policy  string `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, route := range result.Data {
		seen[route.Pattern] = route.Policy
	}
	for _, pattern := range []string{"GET /api/customers", "GET /api/customers/row", "GET /api/customers/rows/{id}"} {
		if seen[pattern] != "permission:customers.read" {
			t.Fatalf("missing protected route %s: %s", pattern, out.String())
		}
	}
	if seen["POST /api/auth/logout"] != "authenticated" || seen["GET /healthz"] != "public" {
		t.Fatal("platform route policies missing", out.String())
	}
}

func TestNewPreviewAndApplyWithoutDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "not-a-database")
	root := t.TempDir()
	raw, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "ddp.yaml")
	definition := filepath.Join(root, "definition.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definition, []byte("purpose: Check records\naction: check\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"new", "job", "check_records", "--definition", definition, "--config", path, "--json"}
	var out, errOut bytes.Buffer
	if code := Execute(t.Context(), append(append([]string{}, args...), "--dry-run"), &out, &errOut, nil); code != 0 {
		t.Fatalf("preview: %d %s %s", code, out.String(), errOut.String())
	}
	var preview struct {
		Data struct {
			Ref      string            `json:"ref"`
			Registry string            `json:"registry"`
			Files    map[string]string `json:"files"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Data.Ref != "job/check_records" || !strings.Contains(preview.Data.Registry, "python: jobs.check_records") || !strings.Contains(preview.Data.Files["jobs/check_records.py"], "from ddp import JobContext") {
		t.Fatalf("preview must expose reviewable text: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "jobs")); !os.IsNotExist(err) {
		t.Fatal("preview wrote code", err)
	}
	out.Reset()
	if code := Execute(t.Context(), args, &out, &errOut, nil); code != 0 {
		t.Fatalf("apply: %d %s", code, out.String())
	}
	file, err := os.ReadFile(filepath.Join(root, "jobs/check_records.py"))
	if err != nil || string(file) != preview.Data.Files["jobs/check_records.py"] {
		t.Fatal("apply differs from preview", err)
	}
	written, err := os.ReadFile(path)
	if err != nil || string(written) != preview.Data.Registry {
		t.Fatal("registry differs from preview", err)
	}
	out.Reset()
	if code := Execute(t.Context(), args, &out, &errOut, nil); code != 1 || !strings.Contains(out.String(), "duplicate") {
		t.Fatalf("duplicate accepted: %d %s", code, out.String())
	}
}

func TestRegistryCommandsUseDeclaredFactsWithoutDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "not-a-database")
	for _, command := range [][]string{{"registry"}, {"search", "CUSTOMERS"}, {"search", "no_such_resource"}} {
		var out, errOut bytes.Buffer
		args := append(command, "--config", "../testdata/reporting/ddp.yaml", "--json")
		if code := Execute(context.Background(), args, &out, &errOut, nil); code != 0 {
			t.Fatalf("%v code %d: %s %s", command, code, &out, &errOut)
		}
		var response struct {
			OK   bool            `json:"ok"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.OK {
			t.Fatalf("invalid response: %s (%v)", out.String(), err)
		}
		if command[0] == "registry" {
			var declared registry.Registry
			if err := json.Unmarshal(response.Data, &declared); err != nil {
				t.Fatal(err)
			}
			if len(declared.Resources) != 11 || !slices.Contains(declared.Types, "permission") {
				t.Fatalf("unexpected registry: %+v", declared)
			}
		} else {
			var resources []registry.Resource
			if err := json.Unmarshal(response.Data, &resources); err != nil || resources == nil {
				t.Fatalf("search result must be an array: %s (%v)", response.Data, err)
			}
			if (len(resources) > 0) != (command[1] == "CUSTOMERS") {
				t.Fatalf("unexpected matches: %+v", resources)
			}
		}
	}
}

func TestSideEffectingRunRequiresMatchingStrategyBeforeConnecting(t *testing.T) {
	root := t.TempDir()
	raw, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	registry := strings.Replace(string(raw), "jobs: {}", "jobs:\n  send:\n    purpose: Send synthetic report.\n    action: notify\n    python: jobs.send\n    idempotency: { strategy: natural_key }", 1)
	path := filepath.Join(root, "ddp.yaml")
	if err := os.WriteFile(path, []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jobs/send.py"), []byte("def run(ctx): pass\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "://")
	for _, strategy := range []string{"", "reconcile", "natural_key"} {
		args := []string{"jobs", "run", "job/send", "--config", path, "--json"}
		if strategy != "" {
			args = append(args, "--confirm-idempotency", strategy)
		}
		var out, errOut bytes.Buffer
		code := Execute(t.Context(), args, &out, &errOut, nil)
		if strategy != "natural_key" {
			if code != 4 || !strings.Contains(out.String(), "requires --confirm-idempotency") {
				t.Fatalf("confirmation bypass: %d %s", code, out.String())
			}
		} else if code != 1 || strings.Contains(out.String(), "requires --confirm-idempotency") {
			t.Fatalf("matching strategy rejected: %d %s", code, out.String())
		}
	}
}

func TestBackfillPreviewAndConfirmationBeforeDatabase(t *testing.T) {
	root := t.TempDir()
	raw, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	registry := strings.Replace(string(raw), "jobs: {}", `jobs:
  send:
    purpose: Send a synthetic report.
    action: notify
    python: jobs.send
    schedule: "0 * * * *"
    idempotency: { strategy: natural_key }`, 1)
	path := filepath.Join(root, "ddp.yaml")
	if err := os.WriteFile(path, []byte(registry), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jobs/send.py"), []byte("def run(ctx): pass\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "://")
	args := []string{"jobs", "backfill", "job/send", "--config", path, "--from", "2025-01-01T00:00:00Z", "--through", "2025-01-01T01:00:00Z", "--json"}
	var out, errOut bytes.Buffer
	if code := Execute(t.Context(), args, &out, &errOut, nil); code != 0 {
		t.Fatalf("DB-free preview: %d %s", code, out.String())
	}
	var result struct {
		Data struct {
			Occurrences int      `json:"occurrences"`
			Executions  int      `json:"executions"`
			Strategies  []string `json:"strategies"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Data.Executions != 2 || result.Data.Occurrences != 2 || !slices.Equal(result.Data.Strategies, []string{"natural_key"}) {
		t.Fatalf("preview result: %+v %v", result, err)
	}
	for _, tc := range []struct {
		flags []string
		want  int
		text  string
	}{
		{[]string{"--apply"}, 4, "--confirm-executions 2"},
		{[]string{"--apply", "--confirm-executions", "2"}, 4, "--confirm-idempotency"},
		{[]string{"--apply", "--confirm-executions", "2", "--confirm-idempotency", "natural_key"}, 1, "invalid DATABASE_URL"},
	} {
		out.Reset()
		errOut.Reset()
		input := append(append([]string{}, args...), tc.flags...)
		if code := Execute(t.Context(), input, &out, &errOut, nil); code != tc.want || !strings.Contains(out.String(), tc.text) {
			t.Fatalf("backfill flags %v: %d %s", tc.flags, code, out.String())
		}
	}
	out.Reset()
	errOut.Reset()
	if code := Execute(t.Context(), []string{"jobs", "pause", "--json"}, &out, &errOut, nil); code != 2 {
		t.Fatalf("missing target: %d %s", code, out.String())
	}
}
