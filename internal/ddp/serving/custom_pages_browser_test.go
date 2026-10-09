package serving

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/models"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
)

func TestCustomPagesBrowserAgainstPostgres(t *testing.T) {
	if os.Getenv("DDP_CUSTOM_BROWSER") != "1" {
		t.Skip("run by check-custom-pages.py in a scaffolded synthetic checkout")
	}
	env := accountTestEnv(t)
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(root, "ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pages["custom_report"].Kind != "custom" {
		t.Fatal("run this test against the scaffolded custom-page registry and build")
	}
	for _, sql := range []string{
		`INSERT INTO app.permissions(name) VALUES('customers.read')`,
		`INSERT INTO app.role_permissions(role_id,permission) VALUES('manager','customers.read')`,
	} {
		if _, err := env.owner.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO synthetic.customers VALUES ('one','{"name":"Synthetic Customer"}',now(),'one')`); err != nil {
		t.Fatal(err)
	}
	if _, err := models.Apply(t.Context(), env.owner, cfg, root); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	cfg.Serving.PublicURL = "http://" + server.Listener.Addr().String()
	var observations atomic.Int64
	handler, err := PortalHandler(env.api, cfg, nil, func(reg *web.Registry) {
		reg.Handle("GET /api/custom-proof", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.Write(w, 200, map[string]any{"rows": []map[string]int64{{"count": observations.Add(1)}}, "next_cursor": nil})
		}), web.Permission("customers.read"))
	})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	for _, script := range []string{"custom-pages.browser.mjs", "branding.browser.mjs"} {
		command := exec.CommandContext(t.Context(), "node", filepath.Join(root, "frontend/apps/portal/tests", script))
		command.Env = append(os.Environ(), "DDP_BASE_URL="+server.URL)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", script, err, output)
		}
	}
}
