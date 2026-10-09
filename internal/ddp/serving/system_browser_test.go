package serving

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/models"
	"github.com/oklog/ulid/v2"
)

func TestSystemBrowserAgainstPostgres(t *testing.T) {
	if os.Getenv("DDP_SYSTEM_BROWSER") != "1" {
		t.Skip("run by make check-smoke with installed Playwright Chromium")
	}
	env := accountTestEnv(t)
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE SCHEMA synthetic`,
		`CREATE TABLE synthetic.customers(id text PRIMARY KEY,payload jsonb NOT NULL,_loaded_at timestamptz NOT NULL,_source_key text NOT NULL)`,
		`INSERT INTO synthetic.customers VALUES('one','{"name":"Console Customer"}',now(),'one')`,
		`INSERT INTO app.permissions(name) VALUES('customers.read')`,
		`INSERT INTO app.role_permissions(role_id,permission) VALUES('manager','customers.read')`,
	} {
		if _, err := env.owner.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := models.Apply(t.Context(), env.owner, env.cfg, filepath.Join(root, "internal/ddp/testdata/reporting")); err != nil {
		t.Fatal(err)
	}
	runID := ulid.Make().String()
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.executions(id,job_ref,scheduled_at,finished_at,status) VALUES($1,'job/sync_customers',now(),now(),'failed'),($2,'ddp:health',now()-interval '1 hour',now()-interval '1 hour','succeeded')`, runID, ulid.Make().String()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.attempts(id,execution_id,number,status,stdout,stderr,error) VALUES($1,$2,1,'failed','<script>console fixture</script>','Synthetic upstream unavailable','Synthetic failure')`, ulid.Make().String(), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.health_evaluations(id,rule_ref,observed_at,observation,state,severity) VALUES($1,'ddp:scheduler',now()-interval '1 hour','{"ref":"ddp:scheduler","state":"failing","severity":"critical","message":"Synthetic scheduler heartbeat missing","notify":[]}','failing','critical')`, ulid.Make().String()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.alert_state(rule_ref,state,evaluation_id) SELECT rule_ref,state,id FROM ops.health_evaluations`); err != nil {
		t.Fatal(err)
	}
	env.cfg.Serving.PublicURL = "http://" + server.Listener.Addr().String()
	handler, err := PortalHandler(env.api, env.cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	command := exec.CommandContext(t.Context(), "node", filepath.Join(root, "frontend/apps/portal/tests/system.browser.mjs"))
	command.Env = append(os.Environ(), "DDP_BASE_URL="+server.URL, "DDP_SYSTEM_RUN="+runID)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("system browser: %v\n%s", err, output)
	}
}
