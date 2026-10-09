package serving

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccountInvitationAndPasswordCompletionAgainstPostgres(t *testing.T) {
	env := accountTestEnv(t)
	admin := portalClient(t)
	accountLogin(t, env.server, admin, "admin@example.test", "admin-password")

	unauthenticated := portalRequest(t, portalClient(t), http.MethodPost, env.server.URL+"/api/users/invite", `{"email":"new@example.test","roles":["reader"]}`, env.origin)
	if unauthenticated.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated invite: %d %s", unauthenticated.status, unauthenticated.body)
	}
	if got := portalRequest(t, admin, http.MethodPost, env.server.URL+"/api/users/invite", `{"email":"new@example.test","roles":["reader"]}`, env.origin); got.status != http.StatusForbidden {
		t.Fatalf("invite without CSRF: %d %s", got.status, got.body)
	}

	manager := portalClient(t)
	managerSession := accountLogin(t, env.server, manager, "manager@example.test", "manager-password")
	if got := portalRequest(t, manager, http.MethodPost, env.server.URL+"/api/users/invite", `{"email":"new@example.test","roles":["reader"]}`, env.origin, managerSession.CSRFToken); got.status != http.StatusCreated {
		t.Fatalf("invite: %d %s", got.status, got.body)
	}

	token := accountOutboxToken(t, env.owner, "welcome", "new@example.test")
	if len(token) != 43 {
		t.Fatalf("welcome token length = %d, want 43", len(token))
	}
	var welcomeURL string
	if err := env.owner.QueryRow(t.Context(), `SELECT context->>'LoginURL' FROM ops.outbox WHERE template='welcome' AND recipients @> ARRAY['new@example.test']::text[] ORDER BY created_at DESC LIMIT 1`).Scan(&welcomeURL); err != nil {
		t.Fatal(err)
	}
	if welcomeURL != env.cfg.Serving.PublicURL+"/welcome#token="+token {
		t.Fatalf("welcome URL = %q", welcomeURL)
	}

	complete := portalRequest(t, portalClient(t), http.MethodPost, env.server.URL+"/api/auth/password", `{"token":"`+token+`","password":"new-password"}`, env.origin)
	if complete.status != http.StatusOK || !strings.Contains(complete.body, `"changed":true`) {
		t.Fatalf("complete invitation: %d %s", complete.status, complete.body)
	}
	if got := accountLogin(t, env.server, portalClient(t), "new@example.test", "new-password"); got.User.Email != "new@example.test" {
		t.Fatalf("new user login: %+v", got.User)
	}
	if got := portalRequest(t, portalClient(t), http.MethodPost, env.server.URL+"/api/auth/password", `{"token":"`+token+`","password":"another-password"}`, env.origin); got.status != http.StatusBadRequest {
		t.Fatalf("reused welcome token: %d %s", got.status, got.body)
	}
}

func TestAccountResetIsGenericOneUseAndRevokesSessionsAgainstPostgres(t *testing.T) {
	env := accountTestEnv(t)
	client := portalClient(t)
	accountLogin(t, env.server, client, "user@example.test", "user-password")
	reset := func(email string) portalResponse {
		return portalRequest(t, portalClient(t), http.MethodPost, env.server.URL+"/api/auth/reset-request", `{"email":"`+email+`"}`, env.origin)
	}
	for _, email := range []string{"user@example.test", "unknown@example.test"} {
		if got := reset(email); got.status != http.StatusAccepted || !acceptedReset(got.body) {
			t.Fatalf("reset request %s: %d %q", email, got.status, got.body)
		}
	}
	if got := reset("user@example.test"); got.status != http.StatusAccepted || !acceptedReset(got.body) {
		t.Fatalf("reset cooldown: %d %q", got.status, got.body)
	}
	if _, err := env.owner.Exec(t.Context(), `UPDATE app.users SET disabled_at=now() WHERE email='disabled@example.test'`); err != nil {
		t.Fatal(err)
	}
	if got := reset("disabled@example.test"); got.status != http.StatusAccepted || !acceptedReset(got.body) {
		t.Fatalf("disabled reset request: %d %q", got.status, got.body)
	}

	token := accountOutboxToken(t, env.owner, "password-reset", "user@example.test")
	if got := portalRequest(t, portalClient(t), http.MethodPost, env.server.URL+"/api/auth/password", `{"token":"`+token+`","password":"reset-password"}`, env.origin); got.status != http.StatusOK {
		t.Fatalf("reset password: %d %s", got.status, got.body)
	}
	if got := portalRequest(t, client, http.MethodGet, env.server.URL+"/api/auth/session", "", ""); got.status != http.StatusUnauthorized {
		t.Fatalf("session after password reset: %d %s", got.status, got.body)
	}
	if got := accountLogin(t, env.server, portalClient(t), "user@example.test", "reset-password"); got.User.Email != "user@example.test" {
		t.Fatalf("reset login: %+v", got.User)
	}
	if got := portalRequest(t, portalClient(t), http.MethodPost, env.server.URL+"/api/auth/password", `{"token":"`+token+`","password":"third-password"}`, env.origin); got.status != http.StatusBadRequest {
		t.Fatalf("reused reset token: %d %s", got.status, got.body)
	}
	var notices int
	if err := env.owner.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE template='password-changed' AND recipients @> ARRAY['user@example.test']::text[]`).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("password changed notice count=%d err=%v", notices, err)
	}
}

type accountEnv struct {
	api    *pgxpool.Pool
	owner  *pgxpool.Pool
	server *httptest.Server
	cfg    *config.Config
	origin string
}

func accountTestEnv(t *testing.T) accountEnv {
	t.Helper()
	owner := portalTestDB(t)
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
	cfg.Comms.SMTP = &config.SMTP{Addr: "127.0.0.1:2525", From: "noreply@example.test", TLS: "none"}
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO app.roles(id,name) VALUES('reader','reader'),('manager','manager')`, nil},
		{`INSERT INTO app.permissions(name) VALUES('users.manage')`, nil},
	} {
		if _, err := owner.Exec(t.Context(), seed.query, seed.args...); err != nil {
			t.Fatal(err)
		}
	}
	apiConfig := owner.Config()
	apiConfig.ConnConfig.RuntimeParams["role"] = "ddp_api"
	api, err := pgxpool.NewWithConfig(t.Context(), apiConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	if err := auth.New(api, 0).Bootstrap(t.Context(), "admin@example.test", "admin-password"); err != nil {
		t.Fatal(err)
	}
	password, err := auth.HashPassword("manager-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"manager@example.test", "user@example.test", "disabled@example.test"} {
		if _, err := owner.Exec(t.Context(), `INSERT INTO app.users(id,email,password_hash) VALUES($1,$2,$3)`, email, email, password); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(t.Context(), `UPDATE app.users SET password_hash=$1 WHERE email='user@example.test'`, mustHash(t, "user-password")); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), `INSERT INTO app.user_roles(user_id,role_id) SELECT id,'manager' FROM app.users WHERE email='manager@example.test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), `INSERT INTO app.role_permissions(role_id,permission) VALUES('manager','users.manage')`); err != nil {
		t.Fatal(err)
	}
	handler, err := PortalHandler(api, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return accountEnv{api: api, owner: owner, server: server, cfg: cfg, origin: cfg.Serving.PublicURL}
}

func accountLogin(t *testing.T, server *httptest.Server, client *http.Client, email, password string) auth.Session {
	t.Helper()
	got := portalRequest(t, client, http.MethodPost, server.URL+"/api/auth/login", `{"email":"`+email+`","password":"`+password+`"}`, "http://localhost:5173")
	if got.status != http.StatusOK {
		t.Fatalf("login %s: %d %s", email, got.status, got.body)
	}
	var response struct {
		Data auth.Session `json:"data"`
	}
	if err := json.Unmarshal([]byte(got.body), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}

func accountOutboxToken(t *testing.T, pool *pgxpool.Pool, template, recipient string) string {
	t.Helper()
	var token string
	field := "ResetURL"
	if template == "welcome" {
		field = "LoginURL"
	}
	query := `SELECT regexp_replace(context->>$1, '^.*#token=', '') FROM ops.outbox WHERE template=$2 AND recipients @> ARRAY[$3]::text[] ORDER BY created_at DESC LIMIT 1`
	if err := pool.QueryRow(t.Context(), query, field, template, recipient).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return token
}

func acceptedReset(body string) bool {
	var response struct {
		Data struct {
			Accepted bool `json:"accepted"`
		} `json:"data"`
	}
	return json.Unmarshal([]byte(body), &response) == nil && response.Data.Accepted
}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	h, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
