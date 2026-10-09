package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/doctor"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/render"
)

func TestDoctorUnhealthySingleEnvelopeAndSafeErrors(t *testing.T) {
	root := t.TempDir()
	registry := filepath.Join(root, "ddp.yaml")
	if err := os.WriteFile(registry, []byte("SECRET: never-print-registry-value"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "never-print-database-secret")
	t.Setenv("PATH", root)
	var out, errOut bytes.Buffer
	if code := Execute(t.Context(), []string{"doctor", "--config", registry, "--json"}, &out, &errOut, nil); code != 3 {
		t.Fatalf("code=%d %s %s", code, &out, &errOut)
	}
	var envelope struct {
		Version int
		OK      bool
		Data    doctor.Report
		Error   *render.Error
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err, out.String())
	}
	if !envelope.OK || envelope.Version != 1 || envelope.Error != nil || envelope.Data.State != "failing" || errOut.Len() != 0 || strings.Contains(out.String(), "never-print") {
		t.Fatalf("unsafe or invalid report: %s %s", &out, &errOut)
	}
	for _, check := range envelope.Data.Checks {
		if check.State != "ok" && check.Repair == "" {
			t.Fatalf("missing repair: %+v", check)
		}
	}
}
