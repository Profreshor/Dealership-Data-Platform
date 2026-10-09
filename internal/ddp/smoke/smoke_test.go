package smoke

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
)

func validConfig() *config.Config {
	return &config.Config{
		Jobs:      map[string]config.Job{"load": {Action: "ingest", Writes: []config.Write{{Target: "table/staging.rows"}}}},
		Models:    map[string]config.Model{"mart.rows": {Reads: []string{"table/staging.rows"}}},
		Endpoints: map[string]config.Endpoint{"rows": {Reads: []string{"model/mart.rows"}}},
		Pages:     map[string]config.Page{"rows": {Kind: "table", Path: "/rows", Endpoint: "endpoint/rows"}},
	}
}

func TestSmokePlanRejectsMissingAmbiguousAndDisconnectedPaths(t *testing.T) {
	tests := []struct {
		name string
		edit func(*config.Config)
		want string
	}{
		{"missing ingest", func(c *config.Config) { clear(c.Jobs) }, "exactly one ingest job (found 0)"},
		{"ambiguous ingest", func(c *config.Config) { c.Jobs["other"] = c.Jobs["load"] }, "exactly one ingest job (found 2)"},
		{"missing model", func(c *config.Config) { clear(c.Models) }, "exactly one model (found 0)"},
		{"ambiguous model", func(c *config.Config) {
			c.Models["mart.other"] = c.Models["mart.rows"]
			c.Endpoints["other"] = config.Endpoint{Reads: []string{"model/mart.other"}}
			c.Pages["other"] = config.Page{Kind: "table", Path: "/other", Endpoint: "endpoint/other"}
		}, "exactly one model (found 2)"},
		{"missing table page", func(c *config.Config) { clear(c.Pages) }, "exactly one table page (found 0)"},
		{"ambiguous table page", func(c *config.Config) { c.Pages["other"] = c.Pages["rows"] }, "exactly one table page (found 2)"},
		{"disconnected endpoint", func(c *config.Config) { c.Endpoints["rows"] = config.Endpoint{Reads: []string{"table/staging.rows"}} }, "exactly one table page (found 0)"},
		{"disconnected ingest", func(c *config.Config) {
			c.Jobs["load"] = config.Job{Action: "ingest", Writes: []config.Write{{Target: "table/staging.other"}}}
		}, "exactly one model (found 0)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.edit(cfg)
			if _, err := smokePlan(cfg); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("smokePlan() error = %v, want %q", err, test.want)
			}
		})
	}
	if got, err := smokePlan(validConfig()); err != nil || got != (plan{job: "job/load", model: "model/mart.rows", page: "/rows"}) {
		t.Fatalf("smokePlan() = %#v, %v", got, err)
	}
}

func TestSmokePlanFollowsModelChainAndIgnoresUnrelatedModels(t *testing.T) {
	c := validConfig()
	c.Models = map[string]config.Model{
		"staging.base":   {Materialization: "view", Reads: []string{"table/staging.rows"}},
		"core.middle":    {Materialization: "materialized_view", Reads: []string{"model/staging.base"}},
		"mart.final":     {Materialization: "view", Reads: []string{"model/core.middle"}},
		"mart.unrelated": {Materialization: "view", Reads: []string{"table/staging.rows"}},
	}
	c.Endpoints["rows"] = config.Endpoint{Reads: []string{"model/mart.final"}}
	got, err := smokePlan(c)
	if err != nil || got != (plan{job: "job/load", model: "model/mart.final", page: "/rows"}) {
		t.Fatalf("smokePlan() = %#v, %v", got, err)
	}
}

func TestSmokePlanRejectsDisconnectedModelAndCycle(t *testing.T) {
	tests := []struct {
		name   string
		models map[string]config.Model
		want   string
	}{
		{"disconnected", map[string]config.Model{
			"mart.final": {Materialization: "view", Reads: []string{"table/staging.other"}},
		}, "exactly one model (found 0)"},
		{"unknown intermediate", map[string]config.Model{
			"mart.final": {Materialization: "view", Reads: []string{"model/core.missing"}},
		}, "model dependency model/core.missing is unknown"},
		{"cycle", map[string]config.Model{
			"mart.final": {Materialization: "view", Reads: []string{"model/core.loop"}},
			"core.loop":  {Materialization: "view", Reads: []string{"model/mart.final"}},
		}, "dependency cycle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := validConfig()
			c.Models = test.models
			c.Endpoints["rows"] = config.Endpoint{Reads: []string{"model/mart.final"}}
			if _, err := smokePlan(c); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("smokePlan() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDatabaseURLTargetsDatabaseAndPreservesParameters(t *testing.T) {
	got, err := databaseURL("postgres://old:secret@db.example/original?sslmode=require&application_name=smoke&dbname=wrong&user=wrong&password=wrong&host=wrong&role=wrong&options=wrong", "fresh", "job", "p@ss/word")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Path != "/fresh" || u.User.Username() != "job" || password != "p@ss/word" || u.Query().Get("sslmode") != "require" || u.Query().Get("application_name") != "smoke" {
		t.Fatalf("databaseURL() produced wrong target: %s", u.Redacted())
	}
	for _, key := range []string{"dbname", "user", "password", "host", "role", "options"} {
		if u.Query().Has(key) {
			t.Fatalf("databaseURL() retained overriding %s parameter", key)
		}
	}
	parsed, err := pgx.ParseConfig(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Database != "fresh" || parsed.User != "job" || parsed.Password != "p@ss/word" || parsed.Host != "db.example" || parsed.RuntimeParams["role"] != "" || parsed.RuntimeParams["options"] != "" {
		t.Fatalf("pgx parsed an overridden connection target: %s", u.Redacted())
	}
}

func TestRunRejectsConnectionOverridesBeforeConnecting(t *testing.T) {
	for _, key := range []string{"host", "dbname", "password", "user", "Role", "options", "service"} {
		t.Setenv("TEST_DATABASE_URL", "postgres://unused:private-value@localhost/base?"+key+"=override")
		_, err := Run(t.Context(), validConfig(), "ddp.yaml", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "connection overrides are unsupported") || strings.Contains(err.Error(), "private-value") {
			t.Fatalf("override %s was not rejected safely: %v", key, err)
		}
	}
}

func TestScopedEnvironment(t *testing.T) {
	base := []string{"PATH=/bin", "DATABASE_URL=old", "JOB_DATABASE_URL=old"}
	got := withEnv(base, "DATABASE_URL", "new", "JOB_DATABASE_URL", "job")
	if !reflect.DeepEqual(got, []string{"PATH=/bin", "DATABASE_URL=new", "JOB_DATABASE_URL=job"}) {
		t.Fatalf("withEnv() = %#v", got)
	}
}

func TestFreshDatabaseKeepsSharedRolesUnchanged(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	adminCfg, err := pgx.ParseConfig(raw)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatal("connect TEST_DATABASE_URL")
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	snapshot := func() string {
		var value string
		if err := admin.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY rolname), '[]')::text FROM (SELECT a.rolname,a.rolsuper,a.rolinherit,a.rolcreaterole,a.rolcreatedb,a.rolcanlogin,a.rolreplication,a.rolbypassrls,a.rolconnlimit,a.rolpassword,a.rolvaliduntil,(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.setdatabase) FROM pg_db_role_setting s WHERE s.setrole=a.oid) AS settings FROM pg_authid a WHERE a.rolname = ANY($1)) r`, []string{"ddp_owner", "ddp_scheduler", "ddp_job", "ddp_api", "ddp_readonly"}).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	baseline := "ddp_smoke_test_baseline_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{baseline}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	baselineExists := true
	t.Cleanup(func() {
		if baselineExists {
			if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{baseline}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Errorf("drop baseline test database: %v", err)
			}
		}
	})
	baselineCfg := *adminCfg
	baselineCfg.Database = baseline
	baselineConn, err := pgx.ConnectConfig(ctx, &baselineCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, baselineConn); err != nil {
		baselineConn.Close(context.Background())
		t.Fatal(err)
	}
	baselineConn.Close(context.Background())
	if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{baseline}.Sanitize()+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	baselineExists = false
	before := snapshot()
	database := "ddp_smoke_test_" + strings.ToLower(rand.Text())
	login := "ddp_smoke_test_login_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable test database: %v", err)
		}
		if _, err := admin.Exec(cleanupCtx, "DROP ROLE IF EXISTS "+pgx.Identifier{login}.Sanitize()); err != nil {
			t.Errorf("drop temporary test login: %v", err)
		}
	})
	dbCfg := *adminCfg
	dbCfg.Database = database
	conn, err := pgx.ConnectConfig(ctx, &dbCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	conn.Close(context.Background())
	if err := createJobLogin(ctx, admin, login, rand.Text()+rand.Text()); err != nil {
		t.Fatal(err)
	}
	var memberships []string
	if err := admin.QueryRow(ctx, `SELECT coalesce(array_agg(parent.rolname ORDER BY parent.rolname), '{}') FROM pg_auth_members m JOIN pg_roles child ON child.oid=m.member JOIN pg_roles parent ON parent.oid=m.roleid WHERE child.rolname=$1`, login).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(memberships, []string{"ddp_job"}) {
		t.Fatalf("temporary login memberships = %v", memberships)
	}
	for _, role := range []string{"ddp_scheduler", "ddp_api"} {
		pool, err := rolePool(ctx, &dbCfg, role)
		if err != nil {
			t.Fatal(err)
		}
		var current, session string
		if err := pool.QueryRow(ctx, "SELECT current_user, session_user").Scan(&current, &session); err != nil {
			pool.Close()
			t.Fatal(err)
		}
		pool.Close()
		if current != role || session != adminCfg.User {
			t.Fatalf("role pool = current %q session %q", current, session)
		}
	}
	if after := snapshot(); after != before {
		t.Fatal("shared role attributes changed")
	}
}

func TestRunCommandCancelsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	cmd := exec.Command("sh", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done")
	started := time.Now()
	if err := runCommand(ctx, cmd); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runCommand() error = %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("runCommand() did not bound cancellation")
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatal("runCommand() did not reap the command")
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process still exists after cancellation: %v", err)
	}
}
