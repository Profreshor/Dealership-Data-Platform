package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
)

func TestClientRouteDiscoveryAndValidationUseComposition(t *testing.T) {
	t.Setenv("DATABASE_URL", "not-a-database")
	register := func(reg *web.Registry) {
		if reg.Pool != nil {
			t.Fatal("discovery opened a database pool")
		}
		reg.Handle("GET /api/client-report", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { httpx.Write(w, 200, map[string]bool{"client": true}) }), web.Permission("customers.read"))
	}
	var out, errOut bytes.Buffer
	if code := Execute(t.Context(), []string{"routes", "--config", "../testdata/reporting/ddp.yaml", "--json"}, &out, &errOut, register); code != 0 {
		t.Fatalf("routes: %d %s %s", code, &out, &errOut)
	}
	var response struct {
		OK   bool
		Data []web.RouteInfo
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.OK {
		t.Fatalf("route envelope: %s (%v)", &out, err)
	}
	found := false
	for _, route := range response.Data {
		if route.Pattern == "GET /api/client-report" && route.Policy == "permission:customers.read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("client route absent: %s", &out)
	}
	for _, command := range [][]string{{"validate"}, {"config", "validate"}, {"routes"}} {
		out.Reset()
		errOut.Reset()
		invalid := func(reg *web.Registry) { reg.Handle("GET /api/client-report", http.NotFoundHandler(), web.Policy{}) }
		args := append(command, "--config", "../testdata/reporting/ddp.yaml", "--json")
		if code := Execute(t.Context(), args, &out, &errOut, invalid); code != 1 || !strings.Contains(out.String(), "invalid route") {
			t.Fatalf("%v accepted missing client policy: %d %s %s", command, code, &out, &errOut)
		}
	}
}
