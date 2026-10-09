package serving

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
)

func TestClientRoutesComposeOnceAndUseAPIPool(t *testing.T) {
	env := accountTestEnv(t)
	var composed int
	register := func(reg *web.Registry) {
		composed++
		if reg.Pool != env.api {
			t.Errorf("registry pool = %p, want API pool %p", reg.Pool, env.api)
		}
		reg.Handle("GET /api/client-check", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var principal string
			if err := reg.Pool.QueryRow(r.Context(), "SELECT current_user").Scan(&principal); err != nil {
				httpx.Fail(w, 500, "internal_error", "database query failed")
				return
			}
			httpx.Write(w, 200, map[string]string{"current_user": principal})
		}), web.Permission("users.manage"))
		reg.Handle("POST /api/client-check", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.Write(w, 200, map[string]bool{"ok": true})
		}), web.Permission("users.manage"))
		reg.Handle("GET /api/client-admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.Write(w, 200, map[string]bool{"ok": true})
		}), web.Admin())
	}
	handler, err := PortalHandler(env.api, env.cfg, nil, register)
	if err != nil {
		t.Fatal(err)
	}
	if composed != 1 {
		t.Fatalf("client registration count = %d, want 1", composed)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	if got := portalRequest(t, portalClient(t), http.MethodGet, server.URL+"/api/client-check", "", ""); got.status != http.StatusUnauthorized {
		t.Fatalf("anonymous client route: %d %s", got.status, got.body)
	}
	member := portalClient(t)
	accountLogin(t, server, member, "user@example.test", "user-password")
	if got := portalRequest(t, member, http.MethodGet, server.URL+"/api/client-check", "", ""); got.status != http.StatusForbidden {
		t.Fatalf("member client route: %d %s", got.status, got.body)
	}
	manager := portalClient(t)
	accountLogin(t, server, manager, "manager@example.test", "manager-password")
	check := portalRequest(t, manager, http.MethodGet, server.URL+"/api/client-check", "", "")
	if check.status != http.StatusOK || !strings.Contains(check.body, `"current_user":"ddp_api"`) {
		t.Fatalf("manager client route: %d %s", check.status, check.body)
	}
	if got := portalRequest(t, manager, http.MethodGet, server.URL+"/api/client-admin", "", ""); got.status != http.StatusForbidden {
		t.Fatalf("manager admin route: %d %s", got.status, got.body)
	}
	if _, err := env.owner.Exec(t.Context(), "DELETE FROM app.role_permissions WHERE role_id='manager' AND permission='users.manage'"); err != nil {
		t.Fatal(err)
	}
	if got := portalRequest(t, manager, http.MethodGet, server.URL+"/api/client-check", "", ""); got.status != http.StatusForbidden {
		t.Fatalf("revoked permission with existing cookie: %d %s", got.status, got.body)
	}

	if got := portalRequest(t, manager, http.MethodPost, server.URL+"/api/client-check", "{}", env.origin); got.status != http.StatusForbidden {
		t.Fatalf("POST without permission: %d %s", got.status, got.body)
	}
}

func TestClientRouteCSRFOriginHeadersAndUnknownAPI(t *testing.T) {
	env := accountTestEnv(t)
	register := func(reg *web.Registry) {
		reg.Handle("POST /api/client-write", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.Write(w, 200, map[string]bool{"ok": true})
		}), web.Permission("users.manage"))
	}
	handler, err := PortalHandler(env.api, env.cfg, nil, register)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	manager := portalClient(t)
	session := accountLogin(t, server, manager, "manager@example.test", "manager-password")
	if got := portalRequest(t, manager, http.MethodPost, server.URL+"/api/client-write", "{}", env.origin); got.status != http.StatusForbidden {
		t.Fatalf("missing CSRF token: %d %s", got.status, got.body)
	}
	if got := portalRequest(t, manager, http.MethodPost, server.URL+"/api/client-write", "{}", "https://evil.example", session.CSRFToken); got.status != http.StatusForbidden {
		t.Fatalf("wrong origin: %d %s", got.status, got.body)
	}
	ok := portalRequest(t, manager, http.MethodPost, server.URL+"/api/client-write", "{}", env.origin, session.CSRFToken)
	if ok.status != http.StatusOK {
		t.Fatalf("valid CSRF request: %d %s", ok.status, ok.body)
	}
	if got := portalRequest(t, portalClient(t), http.MethodGet, server.URL+"/api/does-not-exist", "", ""); got.status != http.StatusNotFound {
		t.Fatalf("unknown API route: %d %s", got.status, got.body)
	}
	res := portalRequest(t, manager, http.MethodGet, server.URL+"/api/client-write", "", "")
	if res.status != http.StatusNotFound {
		t.Fatalf("wrong method: %d %s", res.status, res.body)
	}
	// Successful API responses are private and must carry the platform security headers.
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/client-write", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", env.origin)
	r.Header.Set("X-CSRF-Token", session.CSRFToken)
	response, err := manager.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("security headers: nosniff=%q cache=%q", response.Header.Get("X-Content-Type-Options"), response.Header.Get("Cache-Control"))
	}
}

func TestRoutesDiscoverClientPoliciesWithNilPool(t *testing.T) {
	cfg := loadServingTestConfig(t)
	var calls int
	routes, err := Routes(cfg, func(reg *web.Registry) {
		calls++
		if reg.Pool != nil {
			t.Errorf("discovery registry pool = %p, want nil", reg.Pool)
		}
		reg.Handle("GET /api/client-discovered", http.NotFoundHandler(), web.Permission("users.manage"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("registration count = %d, want 1", calls)
	}
	var found bool
	for _, route := range routes {
		if route.Pattern == "GET /api/client-discovered" && route.Policy == "permission:users.manage" {
			found = true
		}
	}
	if !found {
		t.Fatalf("client route missing from discovery: %#v", routes)
	}
}

func TestClientRouteStartupValidation(t *testing.T) {
	cfg := loadServingTestConfig(t)
	for name, register := range map[string]func(*web.Registry){
		"duplicate platform route": func(reg *web.Registry) {
			reg.Handle("GET /healthz", http.NotFoundHandler(), web.Public())
		},
		"unpoliced": func(reg *web.Registry) {
			reg.Handle("GET /api/unpoliced", http.NotFoundHandler(), web.Policy{})
		},
		"nil handler": func(reg *web.Registry) {
			reg.Handle("GET /api/nil-handler", nil, web.Public())
		},
		"empty pattern": func(reg *web.Registry) {
			reg.Handle("", http.NotFoundHandler(), web.Public())
		},
		"duplicate client route": func(reg *web.Registry) {
			reg.Handle("GET /api/client-duplicate", http.NotFoundHandler(), web.Public())
			reg.Handle("GET /api/client-duplicate", http.NotFoundHandler(), web.Public())
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PortalHandler(nil, cfg, nil, register); err == nil {
				t.Fatal("invalid client route accepted without database")
			}
		})
	}
	if _, err := PortalHandler(nil, cfg, nil, func(reg *web.Registry) {
		reg.Handle("GET /api/customers", http.NotFoundHandler(), web.Public())
	}); err == nil {
		t.Fatal("client/platform route conflict accepted")
	}
}

func loadServingTestConfig(t *testing.T) *config.Config {
	t.Helper()
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		repo = filepath.Dir(repo)
	}
	cfg, err := config.Load(filepath.Join(repo, "internal/ddp/testdata/reporting/ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
