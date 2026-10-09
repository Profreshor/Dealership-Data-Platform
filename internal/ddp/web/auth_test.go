package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/httpx"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestAuthFlowAgainstPostgres(t *testing.T) {
	pool := testDB(t)
	service := auth.New(pool, time.Hour)
	if err := service.Bootstrap(t.Context(), "Admin@Example.test", "admin-password"); err != nil {
		t.Fatal(err)
	}
	if err := service.Bootstrap(t.Context(), "other@example.test", "other-password"); err == nil {
		t.Fatal("second administrator bootstrap succeeded")
	}
	var admins int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM app.users WHERE is_admin").Scan(&admins); err != nil || admins != 1 {
		t.Fatalf("got %d initial administrators: %v", admins, err)
	}

	guard := &Guard{Auth: service}
	reg := NewRegistry()
	reg.Handle("POST /api/auth/login", http.HandlerFunc(guard.Login), Public())
	reg.Handle("GET /api/auth/session", http.HandlerFunc(guard.Session), Public())
	reg.Handle("POST /api/auth/logout", http.HandlerFunc(guard.Logout), Public())
	reg.Handle("GET /api/allowed", http.HandlerFunc(okHandler), Permission("customers.read"))
	reg.Handle("POST /api/write", http.HandlerFunc(okHandler), Authenticated())
	handler, err := reg.Build(guard)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	adminClient := testClient(t)
	adminResponse := request(t, adminClient, http.MethodPost, server.URL+"/api/auth/login", `{"email":"ADMIN@example.test","password":"admin-password"}`, server.URL, "")
	if adminResponse.status != http.StatusOK {
		t.Fatalf("admin login: %d %s", adminResponse.status, adminResponse.body)
	}
	if strings.Contains(adminResponse.body, "password") || strings.Contains(adminResponse.body, "token_hash") {
		t.Fatalf("authentication secret appeared in JSON: %s", adminResponse.body)
	}
	var login struct {
		OK   bool         `json:"ok"`
		Data auth.Session `json:"data"`
	}
	if err := json.Unmarshal([]byte(adminResponse.body), &login); err != nil {
		t.Fatal(err)
	}
	if !login.OK || !login.Data.User.Admin || login.Data.CSRFToken == "" {
		t.Fatalf("unexpected login response: %#v", login)
	}
	if _, err := ulid.Parse(login.Data.User.ID); err != nil {
		t.Fatalf("user ID is not a ULID: %q", login.Data.User.ID)
	}
	if len(adminResponse.cookies) != 1 || adminResponse.cookies[0].Name != "ddp_session" || !adminResponse.cookies[0].HttpOnly || adminResponse.cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookie: %#v", adminResponse.cookies)
	}
	token := adminResponse.cookies[0].Value
	if strings.Contains(adminResponse.body, token) {
		t.Fatal("session token appeared in JSON")
	}
	var stored []byte
	if err := pool.QueryRow(t.Context(), "SELECT token_hash FROM app.sessions WHERE user_id=$1", login.Data.User.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(stored, []byte(token)) || !bytes.Equal(stored, auth.HashToken(token)) {
		t.Fatal("session token was not stored solely as its hash")
	}
	if got := request(t, adminClient, http.MethodGet, server.URL+"/api/auth/session", "", "", ""); got.status != http.StatusOK {
		t.Fatalf("session: %d %s", got.status, got.body)
	}
	if got := request(t, adminClient, http.MethodGet, server.URL+"/api/allowed", "", "", ""); got.status != http.StatusOK {
		t.Fatalf("administrator permission route: %d %s", got.status, got.body)
	}

	userHash, err := auth.HashPassword("user-password")
	if err != nil {
		t.Fatal(err)
	}
	userID := ulid.Make().String()
	roleID := ulid.Make().String()
	batch := &pgx.Batch{}
	batch.Queue("INSERT INTO app.users(id,email,password_hash) VALUES($1,'user@example.test',$2)", userID, userHash)
	batch.Queue("INSERT INTO app.roles(id,name) VALUES($1,'reader')", roleID)
	batch.Queue("INSERT INTO app.permissions(name) VALUES('customers.read')")
	batch.Queue("INSERT INTO app.user_roles(user_id,role_id) VALUES($1,$2)", userID, roleID)
	batch.Queue("INSERT INTO app.role_permissions(role_id,permission) VALUES($1,'customers.read')", roleID)
	results := pool.SendBatch(t.Context(), batch)
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
	userClient := testClient(t)
	userResponse := request(t, userClient, http.MethodPost, server.URL+"/api/auth/login", `{"email":"user@example.test","password":"user-password"}`, server.URL, "")
	if userResponse.status != http.StatusOK {
		t.Fatalf("user login: %d %s", userResponse.status, userResponse.body)
	}
	var userLogin struct {
		Data auth.Session `json:"data"`
	}
	if err := json.Unmarshal([]byte(userResponse.body), &userLogin); err != nil {
		t.Fatal(err)
	}
	oldToken := userResponse.cookies[0].Value
	otherClient := testClient(t)
	otherResponse := request(t, otherClient, http.MethodPost, server.URL+"/api/auth/login", `{"email":"user@example.test","password":"user-password"}`, server.URL, "")
	if otherResponse.status != http.StatusOK {
		t.Fatalf("other-device login: %d %s", otherResponse.status, otherResponse.body)
	}
	otherToken := otherResponse.cookies[0].Value
	rotated := request(t, userClient, http.MethodPost, server.URL+"/api/auth/login", `{"email":"user@example.test","password":"user-password"}`, server.URL, "")
	if rotated.status != http.StatusOK || len(rotated.cookies) != 1 {
		t.Fatalf("rotated login: %d %s", rotated.status, rotated.body)
	}
	newToken := rotated.cookies[0].Value
	if oldToken == newToken {
		t.Fatal("login reused the existing session token")
	}
	if _, err := service.Session(t.Context(), oldToken); err == nil {
		t.Fatal("old browser session survived login rotation")
	}
	if _, err := service.Session(t.Context(), newToken); err != nil {
		t.Fatalf("new browser session is invalid: %v", err)
	}
	if _, err := service.Session(t.Context(), otherToken); err != nil {
		t.Fatalf("other-device session was revoked: %v", err)
	}
	if err := json.Unmarshal([]byte(rotated.body), &userLogin); err != nil {
		t.Fatal(err)
	}
	if got := request(t, userClient, http.MethodGet, server.URL+"/api/allowed", "", "", ""); got.status != http.StatusOK {
		t.Fatalf("granted permission: %d %s", got.status, got.body)
	}
	if _, err := pool.Exec(t.Context(), "DELETE FROM app.role_permissions WHERE role_id=$1", roleID); err != nil {
		t.Fatal(err)
	}
	if got := request(t, userClient, http.MethodGet, server.URL+"/api/allowed", "", "", ""); got.status != http.StatusForbidden {
		t.Fatalf("revoked permission: %d %s", got.status, got.body)
	}
	if got := request(t, userClient, http.MethodPost, server.URL+"/api/write", "", server.URL, ""); got.status != http.StatusForbidden {
		t.Fatalf("missing CSRF token: %d %s", got.status, got.body)
	}
	if got := request(t, userClient, http.MethodPost, server.URL+"/api/write", "", "https://foreign.example", userLogin.Data.CSRFToken); got.status != http.StatusForbidden {
		t.Fatalf("foreign Origin: %d %s", got.status, got.body)
	}
	if got := request(t, userClient, http.MethodPost, server.URL+"/api/write", "", server.URL, userLogin.Data.CSRFToken); got.status != http.StatusOK {
		t.Fatalf("valid CSRF request: %d %s", got.status, got.body)
	}

	wrong := request(t, testClient(t), http.MethodPost, server.URL+"/api/auth/login", `{"email":"user@example.test","password":"wrong-password"}`, server.URL, "")
	missing := request(t, testClient(t), http.MethodPost, server.URL+"/api/auth/login", `{"email":"missing@example.test","password":"wrong-password"}`, server.URL, "")
	if wrong.status != http.StatusUnauthorized || missing.status != http.StatusUnauthorized || wrong.body != missing.body {
		t.Fatalf("credential failures differ: wrong=%d %s missing=%d %s", wrong.status, wrong.body, missing.status, missing.body)
	}
	foreignLogin := request(t, testClient(t), http.MethodPost, server.URL+"/api/auth/login", `{"email":"user@example.test","password":"user-password"}`, "http://localhost.evil", "")
	if foreignLogin.status != http.StatusForbidden || len(foreignLogin.cookies) != 0 {
		t.Fatalf("foreign login Origin: %d %s", foreignLogin.status, foreignLogin.body)
	}

	if got := request(t, userClient, http.MethodPost, server.URL+"/api/auth/logout", "", server.URL, ""); got.status != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d %s", got.status, got.body)
	}
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION app.reject_session_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'delete blocked'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `CREATE TRIGGER reject_session_delete BEFORE DELETE ON app.sessions FOR EACH ROW EXECUTE FUNCTION app.reject_session_delete()`); err != nil {
		t.Fatal(err)
	}
	failedLogout := request(t, userClient, http.MethodPost, server.URL+"/api/auth/logout", "", server.URL, userLogin.Data.CSRFToken)
	if failedLogout.status != http.StatusInternalServerError || len(failedLogout.cookies) != 0 {
		t.Fatalf("failed session revocation was hidden: %d %s cookies=%#v", failedLogout.status, failedLogout.body, failedLogout.cookies)
	}
	if got := request(t, userClient, http.MethodGet, server.URL+"/api/auth/session", "", "", ""); got.status != http.StatusOK {
		t.Fatalf("failed logout cleared live session: %d %s", got.status, got.body)
	}
	if _, err := pool.Exec(t.Context(), "DROP TRIGGER reject_session_delete ON app.sessions"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "DROP FUNCTION app.reject_session_delete()"); err != nil {
		t.Fatal(err)
	}
	logout := request(t, userClient, http.MethodPost, server.URL+"/api/auth/logout", "", server.URL, userLogin.Data.CSRFToken)
	if logout.status != http.StatusOK {
		t.Fatalf("logout: %d %s", logout.status, logout.body)
	}
	if len(logout.cookies) != 1 || logout.cookies[0].MaxAge >= 0 || logout.cookies[0].Expires.IsZero() || !logout.cookies[0].Expires.Before(time.Now()) {
		t.Fatalf("logout did not expire cookie: %#v", logout.cookies)
	}
	if got := request(t, userClient, http.MethodGet, server.URL+"/api/auth/session", "", "", ""); got.status != http.StatusUnauthorized {
		t.Fatalf("session survived logout: %d %s", got.status, got.body)
	}
	var sessions int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM app.sessions WHERE user_id=$1", userID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("got %d sessions after logout; expected the other device only: %v", sessions, err)
	}
}

func TestRegistryRejectsInvalidRoutes(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		handler http.Handler
		policy  Policy
		twice   bool
	}{
		{name: "unpoliced", pattern: "GET /x", handler: http.HandlerFunc(okHandler)},
		{name: "nil handler", pattern: "GET /x", policy: Public()},
		{name: "invalid pattern", pattern: "GET /{", handler: http.HandlerFunc(okHandler), policy: Public()},
		{name: "duplicate", pattern: "GET /x", handler: http.HandlerFunc(okHandler), policy: Public(), twice: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			reg.Handle(tc.pattern, tc.handler, tc.policy)
			if tc.twice {
				reg.Handle(tc.pattern, tc.handler, tc.policy)
			}
			if handler, err := reg.Build(nil); err == nil || handler != nil {
				t.Fatalf("Build accepted route: handler=%v err=%v", handler, err)
			}
		})
	}
}

func TestLocalOriginRejectsLookalikeHosts(t *testing.T) {
	guard := &Guard{}
	for _, origin := range []string{"http://localhost", "http://localhost:5173", "http://127.0.0.1:3000"} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("Origin", origin)
		if !sameOrigin(guard, r) {
			t.Fatalf("rejected local Origin %q", origin)
		}
	}
	for _, origin := range []string{"https://localhost", "http://localhost.evil", "http://127.0.0.1.evil", "http://user@localhost"} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("Origin", origin)
		if sameOrigin(guard, r) {
			t.Fatalf("accepted foreign Origin %q", origin)
		}
	}
}

type response struct {
	status  int
	body    string
	cookies []*http.Cookie
}

func okHandler(w http.ResponseWriter, _ *http.Request) { httpx.Write(w, http.StatusOK, "ok") }

func testClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func request(t *testing.T, client *http.Client, method, url, body, origin, csrf string) response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, body: string(b), cookies: resp.Cookies()}
}

func testDB(t *testing.T) *pgxpool.Pool {
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
	name := fmt.Sprintf("ddp_auth_%x", []byte(rand.Text())[:10])
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
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, config)
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
