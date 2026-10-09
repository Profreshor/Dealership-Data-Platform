package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUsersDisableAgainstPostgres(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ddp_users_disable_%d", time.Now().UnixNano())
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(t.Context())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(ctx)
	})
	uri, err := url.Parse(raw)
	if err != nil || (uri.Scheme != "postgres" && uri.Scheme != "postgresql") {
		t.Fatal("TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	uri.Path = "/" + name
	owner, err := pgx.Connect(t.Context(), uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(context.Background())
	if err := migrate.Up(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	staffHash, err := auth.HashPassword("staff-password")
	if err != nil {
		t.Fatal(err)
	}
	providerHash, err := auth.HashPassword("provider-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), `INSERT INTO app.roles(id,name) VALUES('reader','Reader')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), `INSERT INTO app.users(id,email,password_hash,is_admin) VALUES('01STAFF0000000000000000000','staff@example.test',$1,false),('01PROVIDER0000000000000000','provider@example.test',$2,true)`, staffHash, providerHash); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), `INSERT INTO app.user_roles(user_id,role_id) VALUES('01STAFF0000000000000000000','reader')`); err != nil {
		t.Fatal(err)
	}

	// The CLI runs with the API database grants, as the api service does in production.
	q := uri.Query()
	q.Set("role", "ddp_api")
	uri.RawQuery = q.Encode()
	t.Setenv("DATABASE_URL", uri.String())
	pool, err := pgxpool.New(t.Context(), uri.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	service := auth.New(pool, time.Hour)
	login := func(email, password string) string {
		t.Helper()
		token, _, err := service.Login(t.Context(), email, password)
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		return token
	}
	staffToken := login("staff@example.test", "staff-password")
	providerToken := login("provider@example.test", "provider-password")

	type report struct {
		auth.DisabledAccount
		Message string `json:"message"`
	}
	run := func(want int, args ...string) (report, string) {
		t.Helper()
		var out, logs bytes.Buffer
		code := Execute(t.Context(), append(append([]string{"users"}, args...), "--json", "--config", "does-not-exist"), &out, &logs, nil)
		var envelope struct {
			OK    bool
			Data  report
			Error *struct{ Code, Message string }
		}
		if code != want || json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.OK != (want == 0) || logs.Len() != 0 {
			t.Fatalf("users %v: code=%d want=%d out=%s logs=%s", args, code, want, &out, &logs)
		}
		if envelope.Error != nil {
			return envelope.Data, envelope.Error.Code + ": " + envelope.Error.Message
		}
		return envelope.Data, ""
	}
	state := func(id string) (disabled, operator bool, sessions, roles int) {
		t.Helper()
		if err := owner.QueryRow(t.Context(), `SELECT disabled_at IS NOT NULL,is_admin,(SELECT count(*) FROM app.sessions WHERE user_id=$1),(SELECT count(*) FROM app.user_roles WHERE user_id=$1) FROM app.users WHERE id=$1`, id).Scan(&disabled, &operator, &sessions, &roles); err != nil {
			t.Fatal(err)
		}
		return
	}
	audits := func(id string) []map[string]any {
		t.Helper()
		rows, err := owner.Query(t.Context(), `SELECT outcome FROM ddp.audit WHERE action='users.disable' AND target=$1 AND principal IS NOT NULL ORDER BY id`, "user/"+id)
		if err != nil {
			t.Fatal(err)
		}
		var outcomes []map[string]any
		for rows.Next() {
			var outcome map[string]any
			if err := rows.Scan(&outcome); err != nil {
				t.Fatal(err)
			}
			outcomes = append(outcomes, outcome)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return outcomes
	}

	// An ordinary account is disabled by email without confirmation.
	result, _ := run(0, "disable", "Staff@Example.test")
	if result.ID != "01STAFF0000000000000000000" || !result.Changed || result.OperatorRemoved || !slices.Equal(result.RolesRemoved, []string{"reader"}) || result.RemainingOperators != 1 || !strings.Contains(result.Message, "Disabled staff@example.test") {
		t.Fatalf("ordinary disable: %+v", result)
	}
	if disabled, operator, sessions, roles := state(result.ID); !disabled || operator || sessions != 0 || roles != 0 {
		t.Fatalf("ordinary state: disabled=%t operator=%t sessions=%d roles=%d", disabled, operator, sessions, roles)
	}
	if _, err := service.Session(t.Context(), staffToken); err == nil {
		t.Fatal("disabled account session still works")
	}
	if outcomes := audits(result.ID); len(outcomes) != 1 || outcomes[0]["status"] != "disabled" || outcomes[0]["operator_removed"] != false {
		t.Fatalf("ordinary audit: %v", outcomes)
	}
	result, _ = run(0, "disable", "01STAFF0000000000000000000")
	if result.Changed || !strings.Contains(result.Message, "no change") {
		t.Fatalf("repeat disable: %+v", result)
	}
	if outcomes := audits(result.ID); len(outcomes) != 2 || outcomes[1]["status"] != "unchanged" {
		t.Fatalf("repeat audit: %v", outcomes)
	}

	// Bootstrap refuses while the provider still holds the operator flag.
	t.Setenv("DDP_BOOTSTRAP_PASSWORD", "dealer-password")
	if _, message := run(1, "bootstrap", "--email", "owner@example.test"); !strings.Contains(message, "already bootstrapped") {
		t.Fatalf("bootstrap with operator present: %s", message)
	}

	// An operator account needs --confirm with its exact email.
	for _, flags := range [][]string{nil, {"--confirm", "staff@example.test"}} {
		_, message := run(4, append([]string{"disable", "01PROVIDER0000000000000000"}, flags...)...)
		if !strings.HasPrefix(message, "refused: ") || !strings.Contains(message, "--confirm provider@example.test") {
			t.Fatalf("operator refusal: %s", message)
		}
	}
	if disabled, operator, sessions, _ := state("01PROVIDER0000000000000000"); disabled || !operator || sessions != 1 {
		t.Fatalf("refused disable changed operator: disabled=%t operator=%t sessions=%d", disabled, operator, sessions)
	}
	if _, err := service.Session(t.Context(), providerToken); err != nil {
		t.Fatalf("refused disable revoked session: %v", err)
	}
	if outcomes := audits("01PROVIDER0000000000000000"); len(outcomes) != 0 {
		t.Fatalf("refused disable audited: %v", outcomes)
	}
	result, _ = run(0, "disable", "01PROVIDER0000000000000000", "--confirm", "provider@example.test")
	if !result.Changed || !result.OperatorRemoved || result.RemainingOperators != 0 || !strings.Contains(result.Message, "ddp users bootstrap") {
		t.Fatalf("operator disable: %+v", result)
	}
	if disabled, operator, sessions, _ := state(result.ID); !disabled || operator || sessions != 0 {
		t.Fatalf("operator state: disabled=%t operator=%t sessions=%d", disabled, operator, sessions)
	}
	if _, err := service.Session(t.Context(), providerToken); err == nil {
		t.Fatal("disabled operator session still works")
	}
	if _, _, err := service.Login(t.Context(), "provider@example.test", "provider-password"); err == nil {
		t.Fatal("disabled operator can still sign in")
	}
	if outcomes := audits(result.ID); len(outcomes) != 1 || outcomes[0]["status"] != "disabled" || outcomes[0]["operator_removed"] != true {
		t.Fatalf("operator audit: %v", outcomes)
	}

	// The dealership can now create its own operator.
	run(0, "bootstrap", "--email", "owner@example.test")
	var operators int
	if err := owner.QueryRow(t.Context(), `SELECT count(*) FROM app.users WHERE is_admin AND email='owner@example.test' AND disabled_at IS NULL`).Scan(&operators); err != nil || operators != 1 {
		t.Fatalf("replacement operator count=%d: %v", operators, err)
	}

	// Unknown accounts fail without an audit.
	for _, ref := range []string{"nobody@example.test", "01NOBODY000000000000000000"} {
		if _, message := run(1, "disable", ref); message != "error: account not found" {
			t.Fatalf("unknown account %s: %s", ref, message)
		}
	}
	var total int
	if err := owner.QueryRow(t.Context(), `SELECT count(*) FROM ddp.audit WHERE action='users.disable'`).Scan(&total); err != nil || total != 3 {
		t.Fatalf("users.disable audit count=%d: %v", total, err)
	}
}
