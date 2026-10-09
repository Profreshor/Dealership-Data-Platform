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

func TestAccountBrowserAgainstPostgres(t *testing.T) {
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
	registry, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	registry.Comms, registry.Serving = env.cfg.Comms, env.cfg.Serving
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "ddp.json")
	if err := os.WriteFile(filename, data, 0600); err != nil {
		t.Fatal(err)
	}
	invite := exec.CommandContext(t.Context(), "go", "run", "./cmd/ddp", "--config", filename, "users", "invite", "--email", "browser@example.test", "--role", "reader", "--role", "reader", "--json")
	invite.Dir = root
	databaseURL, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") {
		t.Fatal("browser CLI fixture requires a Postgres URL")
	}
	// ConnString retains the original parsed DSN even after Database is changed.
	databaseURL.Path = "/" + env.api.Config().ConnConfig.Database
	params := databaseURL.Query()
	params.Set("role", "ddp_api")
	databaseURL.RawQuery = params.Encode()
	invite.Env = append(os.Environ(), "DATABASE_URL="+databaseURL.String())
	output, err := invite.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI invite: %v %s", err, output)
	}
	var result struct {
		OK   bool
		Data auth.Invitation
	}
	if err := json.Unmarshal(output, &result); err != nil || !result.OK || result.Data.Status != "invited" || result.Data.Email != "browser@example.test" || len(result.Data.Roles) != 1 || result.Data.Roles[0] != "reader" {
		t.Fatalf("CLI invitation output: %s (%v)", output, err)
	}
	service := auth.New(env.api, 0)
	welcome := accountOutboxToken(t, env.owner, "welcome", "browser@example.test")
	if err := service.RequestReset(t.Context(), env.cfg, "user@example.test"); err != nil {
		t.Fatal(err)
	}
	reset := accountOutboxToken(t, env.owner, "password-reset", "user@example.test")
	command := exec.CommandContext(t.Context(), "node", filepath.Join(root, "frontend/apps/portal/tests/accounts.browser.mjs"))
	command.Env = append(os.Environ(), "DDP_BASE_URL="+server.URL, "DDP_WELCOME_TOKEN="+welcome, "DDP_RESET_TOKEN="+reset)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("account browser: %v\n%s", err, output)
	}
}
