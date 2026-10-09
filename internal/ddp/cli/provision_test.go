package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestProvisionGuardsAndDiscovery(t *testing.T) {
	t.Setenv("DATABASE_URL", "invalid-url-with-secret")
	for _, input := range []struct{ component, password, message string }{
		{"postgres", strings.Repeat("a", 64), "component must be"},
		{"api", "", "API_DATABASE_PASSWORD must"},
		{"api", "secret'; CREATE ROLE injected; --", "API_DATABASE_PASSWORD must"},
		{"api", strings.Repeat("A", 64), "API_DATABASE_PASSWORD must"},
		{"api", strings.Repeat("a", 64), "connect to provisioning database"},
	} {
		t.Setenv("API_DATABASE_PASSWORD", input.password)
		var out, errOut bytes.Buffer
		code := Execute(t.Context(), []string{"provision", input.component, "--config", "missing-registry", "--json"}, &out, &errOut, nil)
		if code == 0 || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), input.message) || strings.Contains(out.String(), "invalid-url-with-secret") || strings.Contains(out.String(), "injected") || errOut.Len() != 0 {
			t.Fatalf("provision guard: %d %s %s", code, &out, &errOut)
		}
	}
	d := discovery(t, "help", "provision")
	if d.Risk != "write" || d.RequiredRole != "administrator" || d.Audit != "transactional: database.provision" {
		t.Fatalf("provision discovery: %+v", d)
	}
}
