package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestBackupCommandGuardsAndDiscovery(t *testing.T) {
	t.Setenv("DATABASE_URL", "never-connect")
	for _, args := range [][]string{
		{"backup", "restore", "latest"},
		{"backup", "restore", "latest", "--database", "production", "--confirm", "wrong"},
		{"backup", "restore", "latest", "--verify", "--database", "production"},
		{"backup", "restore", "latest", "--verify", "--timeout", "0s"},
		{"backup", "restore", "latest", "--database", "new_target", "--confirm", "new_target", "--if-due"},
	} {
		var out, errOut bytes.Buffer
		code := Execute(t.Context(), append(args, "--config", "missing-registry", "--json"), &out, &errOut, nil)
		if code == 0 || !json.Valid(out.Bytes()) || strings.Contains(out.String(), "read registry") || errOut.Len() != 0 {
			t.Fatalf("guard: %v: %d %s %s", args, code, &out, &errOut)
		}
	}
	for _, action := range []string{"list", "run", "restore"} {
		d := discovery(t, "help", "backup", action)
		if action == "list" && d.Risk != "read" {
			t.Fatal("backup list risk")
		}
		if action != "list" && (d.Risk != "external" || d.RequiredRole != "administrator" || !strings.Contains(d.Audit, "durable")) {
			t.Fatal("backup mutation authority")
		}
	}
}
