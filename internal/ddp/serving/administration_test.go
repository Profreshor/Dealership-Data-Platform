package serving

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
)

func TestAdministrationPoliciesAndRevocationAgainstPostgres(t *testing.T) {
	env := accountTestEnv(t)
	manager, member, admin := portalClient(t), portalClient(t), portalClient(t)
	mgr := accountLogin(t, env.server, manager, "manager@example.test", "manager-password")
	accountLogin(t, env.server, member, "user@example.test", "user-password")
	op := accountLogin(t, env.server, admin, "admin@example.test", "admin-password")
	userPath := "/api/users/" + url.PathEscape("user@example.test")
	for _, route := range []struct{ method, path, body string }{
		{"GET", "/api/users", ""},
		{"PUT", userPath, `{"roles":[],"disabled":true}`},
		{"PUT", "/api/roles/reporters", `{"name":"Reporters","permissions":["customers.read"]}`},
	} {
		for _, client := range []struct {
			client *http.Client
			status int
		}{{portalClient(t), 401}, {member, 403}} {
			got := portalRequest(t, client.client, route.method, env.server.URL+route.path, route.body, env.origin)
			if got.status != client.status {
				t.Fatalf("%s %s: %d %s", route.method, route.path, got.status, got.body)
			}
		}
		if route.method == "PUT" {
			for _, security := range []struct{ origin, csrf string }{{env.origin, ""}, {"https://attacker.example", mgr.CSRFToken}} {
				got := portalRequest(t, manager, route.method, env.server.URL+route.path, route.body, security.origin, security.csrf)
				if got.status != 403 {
					t.Fatalf("unguarded change: %d %s", got.status, got.body)
				}
			}
		}
	}
	request := func(path, body string, status int) {
		t.Helper()
		got := portalRequest(t, manager, "PUT", env.server.URL+path, body, env.origin, mgr.CSRFToken)
		if got.status != status {
			t.Fatalf("%s: %d %s", path, got.status, got.body)
		}
	}
	request("/api/roles/reporters", `{"name":"Reporters","permissions":["customers.read"]}`, 200)
	request("/api/roles/duplicate", `{"name":"Reporters","permissions":[]}`, 400)
	request("/api/roles/unknown", `{"name":"Unknown","permissions":["system.admin"]}`, 400)
	request(userPath, `{"roles":[],"disabled":false,"admin":true}`, 400)
	request(userPath, `{"roles":[]}`, 400)
	request(userPath, `{"roles":null,"disabled":false}`, 400)
	request("/api/users/"+op.User.ID, `{"roles":["reporters"],"disabled":true}`, 409)
	request(userPath, `{"roles":["reporters"],"disabled":false}`, 200)
	if got := portalRequest(t, member, "GET", env.server.URL+"/api/auth/session", "", ""); got.status != 401 {
		t.Fatalf("old session survived privilege change: %d", got.status)
	}
	newSession := accountLogin(t, env.server, member, "user@example.test", "user-password")
	if len(newSession.User.Permissions) != 1 || newSession.User.Permissions[0] != "customers.read" {
		t.Fatalf("permissions: %+v", newSession.User)
	}
	request("/api/roles/reporters", `{"name":"Reporters","permissions":[]}`, 200)
	if got := portalRequest(t, member, "GET", env.server.URL+"/api/auth/session", "", ""); got.status != 401 {
		t.Fatal("role edit left session alive")
	}
	request(userPath, `{"roles":[],"disabled":true}`, 200)
	if got := portalRequest(t, portalClient(t), "POST", env.server.URL+"/api/auth/login", `{"email":"user@example.test","password":"user-password"}`, env.origin); got.status != 401 {
		t.Fatal("disabled account logged in")
	}
	request(userPath, `{"roles":[],"disabled":false}`, 200)
	accountLogin(t, env.server, member, "user@example.test", "user-password")
	got := portalRequest(t, manager, "GET", env.server.URL+"/api/users", "", "")
	var result struct{ Data auth.AccountDirectory }
	if got.status != 200 || json.Unmarshal([]byte(got.body), &result) != nil || len(result.Data.Users) != 4 || len(result.Data.Roles) != 3 {
		t.Fatalf("directory: %d %s", got.status, got.body)
	}
	for _, secret := range []string{"password_hash", "csrf_token", "token_hash", "manager-password"} {
		if strings.Contains(got.body, secret) {
			t.Fatalf("directory leaked %s", secret)
		}
	}
	if got := portalRequest(t, manager, "GET", env.server.URL+"/api/system/status", "", ""); got.status != 403 {
		t.Fatalf("client manager gained system access: %d", got.status)
	}
	request("/api/roles/manager", `{"name":"manager","permissions":[]}`, 200)
	if got := portalRequest(t, manager, "GET", env.server.URL+"/api/users", "", ""); got.status != 401 {
		t.Fatal("self-demotion left session alive")
	}
}

func TestAdministrationAuditFailureRollsBackAgainstPostgres(t *testing.T) {
	env := accountTestEnv(t)
	service := auth.New(env.api, 0)
	if _, err := env.owner.Exec(t.Context(), `CREATE FUNCTION app.reject_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private audit fault'; END $$; CREATE TRIGGER reject_admin_audit BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION app.reject_admin_audit()`); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func() error{
		func() error {
			return service.UpdateAccount(t.Context(), "user@example.test", []string{"reader"}, true, "")
		},
		func() error {
			return service.SaveRole(t.Context(), env.cfg, "new_role", "New role", []string{"customers.read"}, "")
		},
	} {
		if err := change(); err == nil || strings.Contains(err.Error(), "private audit fault") {
			t.Fatalf("audit refusal: %v", err)
		}
	}
	var disabled bool
	if err := env.owner.QueryRow(t.Context(), `SELECT disabled_at IS NOT NULL FROM app.users WHERE email='user@example.test'`).Scan(&disabled); err != nil || disabled {
		t.Fatalf("disabled state committed: %v %v", disabled, err)
	}
	var count int
	if err := env.owner.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM app.roles WHERE id='new_role')+(SELECT count(*) FROM app.permissions WHERE name='customers.read')+(SELECT count(*) FROM app.user_roles WHERE user_id='user@example.test')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial writes committed: %d %v", count, err)
	}
}

func TestAdministrationSerializesLoginAndResetAgainstPostgres(t *testing.T) {
	for _, kind := range []string{"account", "role"} {
		t.Run(kind, func(t *testing.T) {
			env := accountTestEnv(t)
			service := auth.New(env.api, 0)
			if err := service.UpdateAccount(t.Context(), "user@example.test", []string{"reader"}, false, ""); err != nil {
				t.Fatal(err)
			}
			token, _, err := service.Login(t.Context(), "user@example.test", "user-password")
			if err != nil {
				t.Fatal(err)
			}
			if err := service.RequestReset(t.Context(), env.cfg, "user@example.test"); err != nil {
				t.Fatal(err)
			}
			resetToken := accountOutboxToken(t, env.owner, "password-reset", "user@example.test")
			// Hold the mutation at its audit insert, after it has locked the user and
			// revoked credentials, so concurrent login/reset must wait for its commit.
			if _, err := env.owner.Exec(t.Context(), `CREATE FUNCTION app.wait_admin_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('users.update','roles.save') THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_database()||'administration-test',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_admin_audit BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION app.wait_admin_audit()`); err != nil {
				t.Fatal(err)
			}
			barrier, err := env.owner.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(t.Context())
			if _, err := barrier.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended(current_database()||'administration-test',0))`); err != nil {
				t.Fatal(err)
			}
			changed := make(chan error, 1)
			go func() {
				if kind == "account" {
					changed <- service.UpdateAccount(t.Context(), "user@example.test", nil, true, "")
				} else {
					changed <- service.SaveRole(t.Context(), env.cfg, "reader", "Reader", []string{"customers.read"}, "")
				}
			}()
			waitLocks := func(want int) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					var n int
					if err := env.owner.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n >= want {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("expected %d database lock waiters", want)
			}
			waitLocks(1)
			loggedIn := make(chan error, 1)
			go func() {
				_, session, err := service.Login(t.Context(), "user@example.test", "user-password")
				if err == nil && kind == "role" && (len(session.User.Permissions) != 1 || session.User.Permissions[0] != "customers.read") {
					err = fmt.Errorf("new login has stale permissions: %+v", session)
				}
				loggedIn <- err
			}()
			reset := make(chan error, 1)
			go func() { reset <- service.SetPassword(t.Context(), env.cfg, resetToken, "replacement-password") }()
			waitLocks(3)
			if err := barrier.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
			loginErr := <-loggedIn
			if (kind == "account" && !errors.Is(loginErr, auth.ErrInvalidCredentials)) || (kind == "role" && loginErr != nil) {
				t.Fatalf("concurrent login: %v", loginErr)
			}
			if err := <-reset; !errors.Is(err, auth.ErrInvalidLink) {
				t.Fatalf("old reset link survived mutation: %v", err)
			}
			if _, err := service.Session(t.Context(), token); err == nil {
				t.Fatal("old session survived mutation")
			}
		})
	}
}
