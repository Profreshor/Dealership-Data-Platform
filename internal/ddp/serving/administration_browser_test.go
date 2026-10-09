package serving

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestAdministrationBrowserAgainstPostgres(t *testing.T) {
	if os.Getenv("DDP_ACCOUNT_BROWSER") != "1" {
		t.Skip("run by make check-smoke with installed Playwright Chromium")
	}
	env := accountTestEnv(t)
	server := httptest.NewUnstartedServer(nil)
	env.cfg.Serving.PublicURL = "http://" + server.Listener.Addr().String()
	handler, err := PortalHandler(env.api, env.cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the machine interface against the same real API-role database first.
	registry := filepath.Join(t.TempDir(), "ddp.json")
	cliConfig, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cliConfig.Comms, cliConfig.Serving, cliConfig.Permissions = env.cfg.Comms, env.cfg.Serving, env.cfg.Permissions
	data, err := json.Marshal(cliConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, data, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "ddp")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/ddp")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	databaseURL, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	databaseURL.Path = "/" + env.api.Config().ConnConfig.Database
	query := databaseURL.Query()
	query.Set("role", "ddp_api")
	databaseURL.RawQuery = query.Encode()
	run := func(args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, append([]string{"--config", registry, "--json"}, args...)...)
		command.Dir = root
		command.Env = append(os.Environ(), "DATABASE_URL="+databaseURL.String())
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
		return out
	}
	run("users", "roles", "save", "cli_reader", "--name", "CLI reader", "--permission", "customers.read")
	run("users", "update", "disabled@example.test", "--role", "cli_reader", "--disabled=true")
	var listed struct {
		OK   bool
		Data auth.AccountDirectory
	}
	if err := json.Unmarshal(run("users", "list"), &listed); err != nil || !listed.OK {
		t.Fatalf("CLI directory: %v", err)
	}
	found := false
	for _, u := range listed.Data.Users {
		if u.Email == "disabled@example.test" {
			found = u.Disabled && len(u.Roles) == 1 && u.Roles[0] == "cli_reader"
		}
	}
	if !found {
		t.Fatal("CLI change missing from directory")
	}
	command := exec.CommandContext(t.Context(), "node", filepath.Join(root, "frontend/apps/portal/tests/administration.browser.mjs"))
	command.Env = append(os.Environ(), "DDP_BASE_URL="+server.URL)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("administration browser: %v\n%s", err, output)
	}
	var count int
	if err := env.owner.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE template='welcome' AND recipients @> ARRAY['invited@example.test']::text[]`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invitation queued %d: %v", count, err)
	}
}
