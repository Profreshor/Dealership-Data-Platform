package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func routeProject(t *testing.T) (string, []byte, []byte) {
	t.Helper()
	root := t.TempDir()
	registry, err := os.ReadFile(filepath.Join("..", "testdata", "reporting", "ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"jobs/sync_customers.py", "models/staging/customers.sql", "models/core/customers.sql", "models/mart/customers.sql"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(path)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte("placeholder\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	aggregator, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "app", "register.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "app", "register.go"), aggregator, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ddp.yaml"), registry, 0644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "ddp.yaml"), registry, aggregator
}

func TestNewRouteDryRunLeavesProjectUnchanged(t *testing.T) {
	registryPath, registry, aggregator := routeProject(t)
	var out, errOut bytes.Buffer
	args := []string{"new", "route", "customer_workflow", "--definition", "-", "--dry-run", "--json", "--config", registryPath}
	// Cobra reads definition paths through the CLI; use a temporary definition file.
	definition := filepath.Join(t.TempDir(), "route.yaml")
	if err := os.WriteFile(definition, []byte("pattern: POST /api/customer-workflow\npolicy: permission:customers.read\n"), 0644); err != nil {
		t.Fatal(err)
	}
	args[4] = definition
	if code := Execute(t.Context(), args, &out, &errOut, nil); code != 0 {
		t.Fatalf("preview: %d %s %s", code, out.String(), errOut.String())
	}
	var response struct {
		OK   bool
		Data struct {
			Kind  string            `json:"kind"`
			Ref   string            `json:"ref"`
			Files map[string]string `json:"files"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.OK {
		t.Fatalf("preview envelope: %s (%v)", out.String(), err)
	}
	if response.Data.Kind != "route" || response.Data.Ref != "route/customer_workflow" {
		t.Fatalf("unexpected proposal: %+v", response.Data)
	}
	for _, path := range []string{"internal/app/customer_workflow/register.go", "internal/app/register.go"} {
		if _, ok := response.Data.Files[path]; !ok {
			t.Fatalf("preview missing %s: %#v", path, response.Data.Files)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(registryPath), "internal", "app", "customer_workflow")); !os.IsNotExist(err) {
		t.Fatal("preview created feature files")
	}
	gotRegistry, _ := os.ReadFile(registryPath)
	gotAggregator, _ := os.ReadFile(filepath.Join(filepath.Dir(registryPath), "internal", "app", "register.go"))
	if !bytes.Equal(gotRegistry, registry) || !bytes.Equal(gotAggregator, aggregator) {
		t.Fatal("preview changed project files")
	}
}

func TestNewRouteApplyUpdatesFeatureAndAggregatorOnly(t *testing.T) {
	registryPath, _, _ := routeProject(t)
	definition := filepath.Join(t.TempDir(), "route.yaml")
	if err := os.WriteFile(definition, []byte("pattern: POST /api/customer-workflow\npolicy: permission:customers.read\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	args := []string{"new", "route", "customer_workflow", "--definition", definition, "--config", registryPath, "--json"}
	if code := Execute(t.Context(), args, &out, &errOut, nil); code != 0 {
		t.Fatalf("apply: %d %s %s", code, out.String(), errOut.String())
	}
	var response struct {
		OK   bool
		Data struct {
			Files           []string `json:"files"`
			RegistryChanged bool     `json:"registry_changed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.OK {
		t.Fatalf("apply envelope: %s (%v)", out.String(), err)
	}
	if !slices.Equal(response.Data.Files, []string{"internal/app/customer_workflow/register.go", "internal/app/register.go"}) || response.Data.RegistryChanged {
		t.Fatalf("unexpected result: %+v", response.Data)
	}
	feature, err := os.ReadFile(filepath.Join(filepath.Dir(registryPath), "internal", "app", "customer_workflow", "register.go"))
	if err != nil || !strings.Contains(string(feature), "POST /api/customer-workflow") || !strings.Contains(string(feature), "StatusNotImplemented") {
		t.Fatalf("generated feature: %s (%v)", feature, err)
	}
}

func TestNewRouteRejectsSource(t *testing.T) {
	registryPath, _, _ := routeProject(t)
	definition := filepath.Join(t.TempDir(), "route.yaml")
	for _, name := range []string{"route.yaml", "source.go"} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(definition), name), []byte("pattern: POST /api/customer-workflow\npolicy: public\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	args := []string{"new", "route", "customer_workflow", "--definition", definition, "--source", filepath.Join(filepath.Dir(definition), "source.go"), "--config", registryPath, "--json"}
	if code := Execute(t.Context(), args, &out, &errOut, nil); code != 2 || !strings.Contains(out.String(), "does not accept --source") {
		t.Fatalf("source accepted: %d %s %s", code, out.String(), errOut.String())
	}
}
