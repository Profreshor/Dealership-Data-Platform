package serving

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/inspect"
	"github.com/oklog/ulid/v2"
)

func TestSystemRoutesAreAdminOnlyAndExposeInspectionFacts(t *testing.T) {
	env := accountTestEnv(t)
	id := ulid.Make().String()
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.executions(id,job_ref,status,scheduled_at,finished_at) VALUES($1,'job/sync_customers','failed',clock_timestamp(),clock_timestamp())`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.attempts(id,execution_id,number,status,finished_at,stdout,stderr,error,payload_expired_at) VALUES($1,$2,1,'failed',clock_timestamp(),'expired stdout','expired stderr','expired error',clock_timestamp())`, ulid.Make().String(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.executions(id,job_ref,status,scheduled_at,finished_at) VALUES($1,'ddp:health','succeeded',clock_timestamp()-interval '1 hour',clock_timestamp()-interval '1 hour')`, ulid.Make().String()); err != nil {
		t.Fatal(err)
	}
	healthID := ulid.Make().String()
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.health_evaluations(id,rule_ref,observed_at,observation,state,severity) VALUES($1,'health/persisted',clock_timestamp()-interval '1 hour','{"ref":"health/persisted","state":"failing","severity":"critical","message":"persisted failure","notify":[]}'::jsonb,'failing','critical')`, healthID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(t.Context(), `INSERT INTO ops.alert_state(rule_ref,state,evaluation_id) VALUES('health/persisted','failing',$1)`, healthID); err != nil {
		t.Fatal(err)
	}

	paths := []string{"/api/system/status", "/api/system/runs", "/api/system/runs/" + id + "/logs"}
	for _, path := range paths {
		if got := portalRequest(t, portalClient(t), http.MethodGet, env.server.URL+path, "", ""); got.status != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: %d %s", path, got.status, got.body)
		}
	}
	manager := portalClient(t)
	accountLogin(t, env.server, manager, "manager@example.test", "manager-password")
	for _, path := range paths {
		if got := portalRequest(t, manager, http.MethodGet, env.server.URL+path, "", ""); got.status != http.StatusForbidden {
			t.Fatalf("manager %s: %d %s", path, got.status, got.body)
		}
	}
	user := portalClient(t)
	accountLogin(t, env.server, user, "user@example.test", "user-password")
	for _, path := range paths {
		if got := portalRequest(t, user, http.MethodGet, env.server.URL+path, "", ""); got.status != http.StatusForbidden {
			t.Fatalf("member %s: %d %s", path, got.status, got.body)
		}
	}

	operator := portalClient(t)
	accountLogin(t, env.server, operator, "admin@example.test", "admin-password")
	gotStatus := portalRequest(t, operator, http.MethodGet, env.server.URL+paths[0], "", "")
	if gotStatus.status != http.StatusOK {
		t.Fatalf("status: %d %s", gotStatus.status, gotStatus.body)
	}
	var statusResponse struct {
		Data inspect.SystemStatus `json:"data"`
	}
	if err := json.Unmarshal([]byte(gotStatus.body), &statusResponse); err != nil {
		t.Fatal(err)
	}
	wantStatus, err := inspect.Overview(t.Context(), env.api, env.cfg)
	if err != nil {
		t.Fatal(err)
	}
	statusResponse.Data.ObservedAt = wantStatus.ObservedAt
	if statusResponse.Data.HealthObservation != "stale" || len(statusResponse.Data.Health) != 1 || statusResponse.Data.Health[0].Observation.State != "failing" {
		t.Fatalf("persisted status facts: %+v", statusResponse.Data)
	}
	statusResponse.Data.ObservedAt = time.Time{}
	wantStatus.ObservedAt = time.Time{}
	gotStatusJSON, _ := json.Marshal(statusResponse.Data)
	wantStatusJSON, _ := json.Marshal(wantStatus)
	if !reflect.DeepEqual(gotStatusJSON, wantStatusJSON) {
		t.Fatalf("status mismatch: got=%s want=%s", gotStatusJSON, wantStatusJSON)
	}
	gotRuns := portalRequest(t, operator, http.MethodGet, env.server.URL+"/api/system/runs", "", "")
	if gotRuns.status != http.StatusOK {
		t.Fatalf("runs: %d %s", gotRuns.status, gotRuns.body)
	}
	var runsResponse struct {
		Data []inspect.Run `json:"data"`
	}
	if err := json.Unmarshal([]byte(gotRuns.body), &runsResponse); err != nil {
		t.Fatal(err)
	}
	wantRuns, err := inspect.ListRuns(t.Context(), env.api, "", 50)
	gotRunsJSON, _ := json.Marshal(runsResponse.Data)
	wantRunsJSON, _ := json.Marshal(wantRuns)
	if err != nil || !reflect.DeepEqual(gotRunsJSON, wantRunsJSON) {
		t.Fatalf("runs mismatch: got=%+v want=%+v err=%v", runsResponse.Data, wantRuns, err)
	}

	gotLogs := portalRequest(t, operator, http.MethodGet, env.server.URL+"/api/system/runs/"+id+"/logs", "", "")
	if gotLogs.status != http.StatusOK || strings.Contains(gotLogs.body, "expired stdout") || strings.Contains(gotLogs.body, "expired error") {
		t.Fatalf("logs: %d %s", gotLogs.status, gotLogs.body)
	}
	var logsResponse struct {
		Data []inspect.AttemptLog `json:"data"`
	}
	if err := json.Unmarshal([]byte(gotLogs.body), &logsResponse); err != nil {
		t.Fatal(err)
	}
	wantLogs, err := inspect.Logs(t.Context(), env.api, "execution/"+id, 100)
	gotLogsJSON, _ := json.Marshal(logsResponse.Data)
	wantLogsJSON, _ := json.Marshal(wantLogs)
	if err != nil || !reflect.DeepEqual(gotLogsJSON, wantLogsJSON) {
		t.Fatalf("logs mismatch: got=%+v want=%+v err=%v", logsResponse.Data, wantLogs, err)
	}
	if got := portalRequest(t, operator, http.MethodGet, env.server.URL+"/api/system/runs/not-a-ulid/logs", "", ""); got.status != http.StatusBadRequest || strings.Contains(got.body, "not-a-ulid") {
		t.Fatalf("invalid run ID: %d %s", got.status, got.body)
	}
	missing := ulid.Make().String()
	if got := portalRequest(t, operator, http.MethodGet, env.server.URL+"/api/system/runs/"+missing+"/logs", "", ""); got.status != http.StatusNotFound || strings.Contains(got.body, "operational history") {
		t.Fatalf("missing run: %d %s", got.status, got.body)
	}
	if _, err := env.owner.Exec(t.Context(), `UPDATE app.users SET disabled_at=clock_timestamp() WHERE email='admin@example.test'`); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if got := portalRequest(t, operator, http.MethodGet, env.server.URL+path, "", ""); got.status != http.StatusUnauthorized {
			t.Fatalf("disabled operator %s: %d %s", path, got.status, got.body)
		}
	}
}

func TestSystemRoutesAreDiscoveredAsAdmin(t *testing.T) {
	cfg := testConfig(t)
	routes, err := Routes(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"GET /api/system/status", "GET /api/system/runs", "GET /api/system/runs/{id}/logs"} {
		found := false
		for _, route := range routes {
			if route.Pattern == pattern {
				found = route.Policy == "admin"
			}
		}
		if !found {
			t.Fatalf("route %s missing or not admin: %+v", pattern, routes)
		}
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
