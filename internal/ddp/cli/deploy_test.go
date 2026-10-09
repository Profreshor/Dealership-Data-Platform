package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeploymentCommandGuardsAndDiscovery(t *testing.T) {
	t.Setenv("DATABASE_URL", "invalid-deployment-url-with-secret")
	for _, args := range [][]string{
		{"deploy", "plan"},
		{"deploy", "record", "--image", "ghcr.io/test/client:stable"},
		{"deploy", "status", "unexpected"},
	} {
		var out, logs bytes.Buffer
		code := Execute(t.Context(), append(args, "--config", "missing-registry", "--json"), &out, &logs, nil)
		if code == 0 || !json.Valid(out.Bytes()) || strings.Contains(out.String(), "read registry") || strings.Contains(out.String(), "with-secret") || logs.Len() != 0 {
			t.Fatalf("guard %v: code=%d output=%s logs=%s", args, code, &out, &logs)
		}
	}
	var out, logs bytes.Buffer
	if code := Execute(t.Context(), []string{"deploy", "status", "--config", "missing-registry", "--json"}, &out, &logs, nil); code == 0 || strings.Contains(out.String(), "with-secret") || strings.Contains(out.String(), "read registry") || !json.Valid(out.Bytes()) || logs.Len() != 0 {
		t.Fatalf("status failure: code=%d out=%s logs=%s", code, &out, &logs)
	}
	for _, action := range []string{"plan", "record", "status"} {
		command := discovery(t, "help", "deploy", action)
		if action == "record" {
			if command.Risk != "write" || command.RequiredRole != "administrator" || !strings.Contains(command.Audit, "transactional") {
				t.Fatalf("record policy: %+v", command)
			}
		} else if command.Risk != "read" {
			t.Fatalf("read policy: %+v", command)
		}
	}
}
