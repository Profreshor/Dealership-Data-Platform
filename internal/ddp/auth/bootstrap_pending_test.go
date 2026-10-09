package auth

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestBootstrapActivatesPendingDiscoveredAccount(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	db := "ddp_pending_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(c, "DROP DATABASE "+pgx.Identifier{db}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(c)
	})
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = db
	owner, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	conn, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	apiCfg := cfg.Copy()
	apiCfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+pgx.Identifier{"ddp_api"}.Sanitize())
		return err
	}
	api, err := pgxpool.NewWithConfig(ctx, apiCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	if _, err := owner.Exec(ctx, `INSERT INTO app.roles(id,name) VALUES ('analyst','analyst'); INSERT INTO app.users(id,email,password_hash,is_admin) VALUES ('pending-user','pending@example.test',NULL,false),('active-user','active@example.test','already-hashed',false),('disabled-user','disabled@example.test',NULL,false); UPDATE app.users SET disabled_at=now() WHERE id='disabled-user'; INSERT INTO app.user_roles(user_id,role_id) VALUES ('pending-user','analyst'); INSERT INTO app.password_tokens(token_hash,user_id,kind) VALUES (decode(repeat('aa',32),'hex'),'pending-user','invite'); INSERT INTO app.sessions(token_hash,user_id,expires_at) VALUES (decode(repeat('bb',32),'hex'),'pending-user',now()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct{ email, password string }{{"active@example.test", "active-password"}, {"disabled@example.test", "disabled-password"}} {
		if err := New(api, time.Hour).Bootstrap(ctx, account.email, account.password); err == nil {
			t.Fatalf("bootstrap unexpectedly accepted %s", account.email)
		}
	}
	if err := New(api, time.Hour).Bootstrap(ctx, "Pending@Example.test", "pending-password"); err != nil {
		t.Fatal(err)
	}
	var id string
	var adminFlag, pending bool
	if err := owner.QueryRow(ctx, `SELECT id,is_admin,password_hash IS NULL FROM app.users WHERE email='pending@example.test'`).Scan(&id, &adminFlag, &pending); err != nil {
		t.Fatal(err)
	}
	if id != "pending-user" || !adminFlag || pending {
		t.Fatalf("pending account not activated: id=%q admin=%v pending=%v", id, adminFlag, pending)
	}
	var tokens, sessions int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM app.password_tokens WHERE user_id='pending-user'`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM app.sessions WHERE user_id='pending-user'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if tokens != 0 || sessions != 0 {
		t.Fatalf("activation left credentials: tokens=%d sessions=%d", tokens, sessions)
	}
	var roles int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM app.user_roles WHERE user_id='pending-user' AND role_id='analyst'`).Scan(&roles); err != nil || roles != 1 {
		t.Fatalf("roles changed: %d %v", roles, err)
	}
	if err := New(api, time.Hour).Bootstrap(ctx, "second@example.test", "second-password"); err == nil {
		t.Fatal("second admin bootstrap succeeded")
	}
	// Reopen bootstrap in this private fixture so the next call reaches the
	// audit trigger after attempting activation and credential revocation.
	if _, err := owner.Exec(ctx, `UPDATE app.users SET is_admin=false WHERE id='pending-user'`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO app.users(id,email,password_hash,is_admin) VALUES ('pending-failure','failure@example.test',NULL,false); INSERT INTO app.password_tokens(token_hash,user_id,kind) VALUES (decode(repeat('cc',32),'hex'),'pending-failure','invite'); INSERT INTO app.sessions(token_hash,user_id,expires_at) VALUES (decode(repeat('dd',32),'hex'),'pending-failure',now()+interval '1 hour'); CREATE FUNCTION app.reject_bootstrap_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'bootstrap audit rejected'; END $$; CREATE TRIGGER reject_bootstrap_test BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION app.reject_bootstrap_test()`); err != nil {
		t.Fatal(err)
	}
	if err := New(api, time.Hour).Bootstrap(ctx, "failure@example.test", "failure-password"); err == nil || err.Error() != "record operation audit" {
		t.Fatalf("audit failure was not reached: %v", err)
	}
	var failurePending, failureTokens, failureSessions int
	if err := owner.QueryRow(ctx, `SELECT count(*) FILTER (WHERE password_hash IS NULL AND NOT is_admin), (SELECT count(*) FROM app.password_tokens WHERE user_id='pending-failure'), (SELECT count(*) FROM app.sessions WHERE user_id='pending-failure') FROM app.users WHERE id='pending-failure'`).Scan(&failurePending, &failureTokens, &failureSessions); err != nil {
		t.Fatal(err)
	}
	if failurePending != 1 || failureTokens != 1 || failureSessions != 1 {
		t.Fatalf("audit failure committed activation: pending=%d tokens=%d sessions=%d", failurePending, failureTokens, failureSessions)
	}
	if _, err := owner.Exec(ctx, `DROP TRIGGER reject_bootstrap_test ON ddp.audit; DROP FUNCTION app.reject_bootstrap_test()`); err != nil {
		t.Fatal(err)
	}
}
