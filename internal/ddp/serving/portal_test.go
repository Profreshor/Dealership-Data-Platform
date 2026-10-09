package serving

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestPortalHandlerAgainstPostgres(t *testing.T) {
	pool := portalTestDB(t)
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
	for _, statement := range []string{
		"CREATE SCHEMA synthetic",
		"CREATE TABLE synthetic.customers (id text PRIMARY KEY, payload jsonb NOT NULL, _loaded_at timestamptz NOT NULL, _source_key text NOT NULL)",
		`INSERT INTO synthetic.customers VALUES ('one', '{"name":"Synthetic Customer"}', now(), 'one'), ('two', '{"name":"Example Customer"}', now(), 'two')`,
	} {
		if _, err := pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := models.Apply(t.Context(), pool, cfg, repo+"/internal/ddp/testdata/reporting"); err != nil {
		t.Fatal(err)
	}
	if err := auth.New(pool, 0).Bootstrap(t.Context(), "admin@example.test", "admin-password"); err != nil {
		t.Fatal(err)
	}
	password, err := auth.HashPassword("user-password")
	if err != nil {
		t.Fatal(err)
	}
	userID, roleID := ulid.Make().String(), ulid.Make().String()
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO app.users(id,email,password_hash) VALUES($1,'user@example.test',$2)`, []any{userID, password}},
		{`INSERT INTO app.roles(id,name) VALUES($1,'reader')`, []any{roleID}},
		{`INSERT INTO app.permissions(name) VALUES('customers.read')`, nil},
		{`INSERT INTO app.user_roles(user_id,role_id) VALUES($1,$2)`, []any{userID, roleID}},
		{`INSERT INTO app.role_permissions(role_id,permission) VALUES($1,'customers.read')`, []any{roleID}},
	} {
		if _, err := pool.Exec(t.Context(), seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	apiConfig := pool.Config()
	apiConfig.ConnConfig.RuntimeParams["role"] = "ddp_api"
	apiPool, err := pgxpool.NewWithConfig(t.Context(), apiConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(apiPool.Close)
	handler, err := PortalHandler(apiPool, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	origin := cfg.Serving.PublicURL

	unauthenticated := portalClient(t)
	for _, path := range []string{"/api/portal", "/api/customers"} {
		res := portalRequest(t, unauthenticated, http.MethodGet, server.URL+path, "", "")
		if res.status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: got %d, want 401: %s", path, res.status, res.body)
		}
	}
	unknown := portalRequest(t, unauthenticated, http.MethodGet, server.URL+"/api/unknown", "", "")
	if unknown.status != http.StatusNotFound || !strings.Contains(unknown.body, `"ok":false`) {
		t.Fatalf("unknown API route: %d %s", unknown.status, unknown.body)
	}
	static := portalRequest(t, unauthenticated, http.MethodGet, server.URL+"/unknown", "", "")
	if static.status != http.StatusOK || !strings.Contains(static.body, "<html") {
		t.Fatalf("embedded page: %d %s", static.status, static.body)
	}
	method := portalRequest(t, unauthenticated, http.MethodPost, server.URL+"/unknown", "", "")
	if method.status != http.StatusMethodNotAllowed {
		t.Fatalf("static method guard: got %d, want 405", method.status)
	}

	admin := portalClient(t)
	adminLogin := portalRequest(t, admin, http.MethodPost, server.URL+"/api/auth/login", `{"email":"admin@example.test","password":"admin-password"}`, origin)
	if adminLogin.status != http.StatusOK {
		t.Fatalf("admin login: %d %s", adminLogin.status, adminLogin.body)
	}
	var adminSession struct {
		Data auth.Session `json:"data"`
	}
	if err := json.Unmarshal([]byte(adminLogin.body), &adminSession); err != nil {
		t.Fatal(err)
	}
	logoutCSRF := adminSession.Data.CSRFToken
	portal := portalRequest(t, admin, http.MethodGet, server.URL+"/api/portal", "", "")
	if portal.status != http.StatusOK || !strings.Contains(portal.body, "Customers") || !strings.Contains(portal.body, "/api/customers") {
		t.Fatalf("admin portal: %d %s", portal.status, portal.body)
	}

	user := portalClient(t)
	login := portalRequest(t, user, http.MethodPost, server.URL+"/api/auth/login", `{"email":"user@example.test","password":"user-password"}`, origin)
	if login.status != http.StatusOK {
		t.Fatalf("user login: %d %s", login.status, login.body)
	}
	portal = portalRequest(t, user, http.MethodGet, server.URL+"/api/portal", "", "")
	if portal.status != http.StatusOK || !strings.Contains(portal.body, "Customers") {
		t.Fatalf("permitted user portal: %d %s", portal.status, portal.body)
	}
	rows := portalRequest(t, user, http.MethodGet, server.URL+"/api/customers", "", "")
	if rows.status != http.StatusOK || !strings.Contains(rows.body, "Synthetic Customer") || !strings.Contains(rows.body, "Example Customer") {
		t.Fatalf("customer table: %d %s", rows.status, rows.body)
	}
	if _, err := pool.Exec(t.Context(), "DELETE FROM app.role_permissions WHERE role_id=$1", roleID); err != nil {
		t.Fatal(err)
	}
	portal = portalRequest(t, user, http.MethodGet, server.URL+"/api/portal", "", "")
	if portal.status != http.StatusOK || strings.Contains(portal.body, "Customers") {
		t.Fatalf("revoked navigation: %d %s", portal.status, portal.body)
	}
	rows = portalRequest(t, user, http.MethodGet, server.URL+"/api/customers", "", "")
	if rows.status != http.StatusForbidden {
		t.Fatalf("revoked endpoint: got %d, want 403: %s", rows.status, rows.body)
	}
	if got := portalRequest(t, admin, http.MethodPost, server.URL+"/api/auth/logout", "", origin); got.status != http.StatusForbidden {
		t.Fatalf("logout without CSRF: got %d, want 403", got.status)
	}
	if got := portalRequest(t, admin, http.MethodPost, server.URL+"/api/auth/logout", "", origin, logoutCSRF); got.status != http.StatusOK {
		t.Fatalf("logout: %d %s", got.status, got.body)
	}
	if got := portalRequest(t, admin, http.MethodGet, server.URL+"/api/auth/session", "", ""); got.status != http.StatusUnauthorized {
		t.Fatalf("revoked session: got %d, want 401", got.status)
	}
}

type portalResponse struct {
	status int
	body   string
}

func portalClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func portalRequest(t *testing.T, client *http.Client, method, url, body string, origin string, csrf ...string) portalResponse {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if len(csrf) > 0 {
		req.Header.Set("X-CSRF-Token", csrf[0])
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return portalResponse{status: res.StatusCode, body: string(b)}
}

func portalTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_portal_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		admin.Close(ctx)
	})
	settings, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	settings.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	return pool
}
