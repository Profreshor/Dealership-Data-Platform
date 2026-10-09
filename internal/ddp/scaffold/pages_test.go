package scaffold

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomPageScaffoldPublishesSourceAndRegistry(t *testing.T) {
	path, original := project(t)
	r := Request{Kind: "page", Name: "overview", Definition: []byte("label: 'Summary </h1><script>alert(1)</script>'\npath: /\nkind: custom\npolicy: admin\n")}
	preview, err := Preview(path, r)
	if err != nil {
		t.Fatal(err)
	}
	source := preview.Files["frontend/apps/portal/src/routes/overview.tsx"]
	if !bytes.Contains(source, []byte(`registerPage("overview", Page)`)) || bytes.Contains(source, []byte("<script>")) {
		t.Fatalf("unsafe or disconnected source: %s", source)
	}
	result, err := Apply(path, r)
	if err != nil || !result.RegistryChanged || len(result.Files) != 1 {
		t.Fatalf("custom page application: %+v (%v)", result, err)
	}
	written, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(written, preview.Registry) || bytes.Equal(written, original) {
		t.Fatalf("registry publication: %s (%v)", written, err)
	}
	writtenSource, err := os.ReadFile(filepath.Join(filepath.Dir(path), result.Files[0]))
	if err != nil || !bytes.Equal(writtenSource, source) {
		t.Fatalf("source publication: %s (%v)", writtenSource, err)
	}
	if _, err := Apply(path, r); err == nil {
		t.Fatal("duplicate page accepted")
	}
	r.Source = []byte("ignored source")
	if _, err := Build(original, r); err == nil || !strings.Contains(err.Error(), "does not accept source") {
		t.Fatalf("custom source accepted: %v", err)
	}
}

func TestSystemPageScaffoldUsesExistingRuntime(t *testing.T) {
	p, err := Build(baseRegistry(t), Request{Kind: "page", Name: "operations", Definition: []byte("label: Operations\npath: /operations\nkind: system\npolicy: admin\n")})
	if err != nil || len(p.Files) != 0 || !bytes.Contains(p.Registry, []byte("operations:")) {
		t.Fatalf("system page scaffold: %+v (%v)", p, err)
	}
}
