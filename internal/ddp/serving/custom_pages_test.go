package serving

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestCustomPageMetadataUsesStableIDAndCurrentPermission(t *testing.T) {
	env := accountTestEnv(t)
	env.cfg.Pages["workflow"] = config.Page{Kind: "custom", Path: "/renamed-workflow", Label: "New workflow label", Permission: "users.manage"}
	handler, err := PortalHandler(env.api, env.cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	manager := portalClient(t)
	accountLogin(t, server, manager, "manager@example.test", "manager-password")
	visible := func() bool {
		t.Helper()
		got := portalRequest(t, manager, http.MethodGet, server.URL+"/api/portal", "", "")
		var response struct {
			Data struct {
				Pages []struct{ ID, Label, Path, Kind string }
			}
		}
		if got.status != http.StatusOK || json.Unmarshal([]byte(got.body), &response) != nil {
			t.Fatalf("metadata: %d %s", got.status, got.body)
		}
		for _, page := range response.Data.Pages {
			if page.ID == "workflow" {
				if page.Path != "/renamed-workflow" || page.Label != "New workflow label" || page.Kind != "custom" {
					t.Fatalf("page identity changed: %+v", page)
				}
				return true
			}
		}
		return false
	}
	if !visible() {
		t.Fatal("permitted custom page missing")
	}
	if _, err := env.owner.Exec(t.Context(), "DELETE FROM app.role_permissions WHERE role_id='manager' AND permission='users.manage'"); err != nil {
		t.Fatal(err)
	}
	if visible() {
		t.Fatal("existing session retained revoked custom page")
	}
}
