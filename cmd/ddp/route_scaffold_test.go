package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/scaffold"
)

func TestScaffoldedRouteIsDiscoveredByBinary(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	for file, fixture := range map[string]string{"ddp.yaml": "internal/ddp/testdata/base/ddp.yaml", "internal/app/register.go": "internal/app/register.go"} {
		source, err := os.ReadFile(filepath.Join(repo, fixture))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(project, file)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, source, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := scaffold.Apply(filepath.Join(project, "ddp.yaml"), scaffold.Request{
		Kind: "route", Name: "scaffold_proof",
		Definition: []byte("pattern: POST /api/scaffold-proof\npolicy: admin\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Compile the real binary with only the scaffold's two Go files overlaid.
	// No test-supplied registration or handler can make this proof pass.
	replacements := map[string]string{}
	for _, file := range result.Files {
		replacements[filepath.Join(repo, file)] = filepath.Join(project, file)
	}
	overlay, err := json.Marshal(map[string]any{"Replace": replacements})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(project, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"validate", "routes"} {
		command := exec.CommandContext(t.Context(), "go", "run", "-overlay", overlayPath, "./cmd/ddp", action, "--config", filepath.Join(project, "ddp.yaml"), "--json")
		command.Dir = repo
		command.Env = append(os.Environ(), "DATABASE_URL=not-a-database")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("scaffold binary %s: %v\n%s", action, err, output)
		}
		if action == "routes" {
			var response struct {
				OK   bool
				Data []struct{ Pattern, Policy string }
			}
			if err := json.Unmarshal(output, &response); err != nil || !response.OK {
				t.Fatalf("routes envelope: %s (%v)", output, err)
			}
			found := false
			for _, route := range response.Data {
				if route.Pattern == "POST /api/scaffold-proof" && route.Policy == "admin" {
					found = true
				}
			}
			if !found {
				t.Fatalf("scaffold is disconnected: %s", output)
			}
		} else if !bytes.Contains(output, []byte(`"valid":true`)) {
			t.Fatalf("validation failed: %s", output)
		}
	}
}
