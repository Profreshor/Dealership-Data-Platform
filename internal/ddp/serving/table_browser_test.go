package serving

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/models"
)

func TestPortalTableMetadataMatchesEndpoint(t *testing.T) {
	env := accountTestEnv(t)
	admin := portalClient(t)
	accountLogin(t, env.server, admin, "admin@example.test", "admin-password")
	got := portalRequest(t, admin, http.MethodGet, env.server.URL+"/api/portal", "", "")
	if got.status != http.StatusOK {
		t.Fatalf("metadata: %d %s", got.status, got.body)
	}
	var result struct {
		Data struct {
			Pages []struct {
				Kind, Endpoint, Shape          string
				Columns, Filters, Sort, Search []string
				PageSize                       int `json:"page_size"`
				Export                         *config.Export
			}
		}
	}
	if err := json.Unmarshal([]byte(got.body), &result); err != nil {
		t.Fatal(err)
	}
	ep := env.cfg.Endpoints["customers"]
	found := false
	for _, page := range result.Data.Pages {
		if page.Kind != "table" {
			continue
		}
		found = true
		if page.Endpoint != ep.Path || !reflect.DeepEqual(page.Columns, ep.Columns) || !reflect.DeepEqual(page.Filters, ep.Filters) || !reflect.DeepEqual(page.Sort, ep.Sort) || !reflect.DeepEqual(page.Search, ep.Search) || page.PageSize != ep.PageSize || page.Shape != "list" || !reflect.DeepEqual(page.Export, ep.Export) {
			t.Fatalf("table metadata diverged: %+v", page)
		}
	}
	if !found {
		t.Fatal("table metadata missing")
	}
}

func TestTableBrowserAgainstPostgres(t *testing.T) {
	if os.Getenv("DDP_TABLE_BROWSER") != "1" {
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
		`INSERT INTO synthetic.customers SELECT id::text,jsonb_build_object('name',name),now(),id::text FROM (VALUES(1,'Alpha Customer'),(2,'Beta Customer'),(3,'Gamma Customer'),(4,'Quote & %_ Customer'),(5,'Zulu Customer'),(6,'')) AS source(id,name)`,
		`CREATE TABLE mart.summary(total integer NOT NULL)`,
		`INSERT INTO mart.summary VALUES(6)`,
		`GRANT SELECT ON mart.summary TO ddp_api`,
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
	endpoint := env.cfg.Endpoints["customers"]
	endpoint.PageSize = 2
	endpoint.Export = &config.Export{Format: "csv", MaxRows: 3}
	env.cfg.Endpoints["customers"] = endpoint
	env.cfg.Tables["mart.summary"] = config.Table{Purpose: "Synthetic customer count.", Contract: config.Contract{Columns: map[string]config.Column{"total": {Type: "integer"}}}}
	env.cfg.Endpoints["summary"] = config.Endpoint{Reads: []string{"table/mart.summary"}, Path: "/api/summary", Policy: "permission:customers.read", Columns: []string{"total"}, Shape: "singleton"}
	env.cfg.Pages["summary"] = config.Page{Label: "Summary", Path: "/summary", Kind: "table", Endpoint: "endpoint/summary", Permission: "customers.read", Order: 2}
	if err := env.cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	env.cfg.Serving.PublicURL = "http://" + server.Listener.Addr().String()
	handler, err := PortalHandler(env.api, env.cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	command := exec.CommandContext(t.Context(), "node", filepath.Join(root, "frontend/apps/portal/tests/table.browser.mjs"))
	command.Env = append(os.Environ(), "DDP_BASE_URL="+server.URL)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("table browser: %v\n%s", err, output)
	}
}
