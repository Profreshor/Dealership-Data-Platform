package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBinaryUsesClientCompositionRoot(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(`package app
import (
 "net/http"
 "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
)
func Register(reg *web.Registry) {
 reg.Handle("GET /api/composition-proof",http.NotFoundHandler(),web.Admin())
}
`)
	replacement := filepath.Join(t.TempDir(), "register.go")
	if err := os.WriteFile(replacement, source, 0600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(root, "internal/app/register.go"): replacement}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(t.TempDir(), "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	// Overlay only the client aggregator. The actual main and all platform files
	// stay unchanged while the binary discovers the added feature.
	command := exec.CommandContext(t.Context(), "go", "run", "-overlay", overlayPath, "./cmd/ddp", "routes", "--config", "internal/ddp/testdata/reporting/ddp.yaml", "--json")
	command.Dir = root
	command.Env = append(os.Environ(), "DATABASE_URL=not-a-database")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("client binary: %v\n%s", err, output)
	}
	var result struct {
		OK   bool
		Data []struct{ Pattern, Policy string }
	}
	if err := json.Unmarshal(output, &result); err != nil || !result.OK {
		t.Fatalf("client route output: %s (%v)", output, err)
	}
	for _, route := range result.Data {
		if route.Pattern == "GET /api/composition-proof" && route.Policy == "admin" {
			return
		}
	}
	t.Fatalf("composition root ignored: %s", output)
}
