package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommsPreviewAndResendBoundary(t *testing.T) {
	t.Setenv("DATABASE_URL", "not-a-database-secret")
	root := t.TempDir()
	base, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "ddp.yaml")
	if err := os.WriteFile(registry, base, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "templates/comms"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"welcome.txt", "welcome.html"} {
		data, err := os.ReadFile(filepath.Join("../../../templates/comms", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "templates/comms", name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	input := filepath.Join(t.TempDir(), "context.json")
	if err := os.WriteFile(input, []byte(`{"Name":"Synthetic Operator","LoginURL":"https://example.test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Execute(t.Context(), []string{"--config", registry, "comms", "preview", "welcome", "--data", input, "--json"}, &out, &errOut, nil)
	var report struct {
		OK   bool
		Data struct{ Subject, Text, HTML string }
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || code != 0 || !report.OK || !strings.Contains(report.Data.Text, "Synthetic Operator") {
		t.Fatalf("preview: %d %s %v", code, out.String(), err)
	}
	out.Reset()
	errOut.Reset()
	code = Execute(t.Context(), []string{"comms", "resend", "message/01ARZ3NDEKTSV4RRFFQ69G5FAV", "--effect-key", "new", "--json"}, &out, &errOut, nil)
	if code != 4 || strings.Contains(out.String(), "not-a-database-secret") || !strings.Contains(out.String(), "--confirm") {
		t.Fatalf("resend was not refused before DB: %d %s", code, out.String())
	}
}
