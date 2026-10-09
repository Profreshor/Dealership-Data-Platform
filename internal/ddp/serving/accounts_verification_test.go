package serving

import (
	"errors"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccountResetLoginOrderingAndAuthTablePrivileges(t *testing.T) {
	env := accountTestEnv(t)
	service := auth.New(envServerAPI(t, env), time.Hour)
	oldToken, _, err := service.Login(t.Context(), "user@example.test", "user-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RequestReset(t.Context(), env.cfg, "user@example.test"); err != nil {
		t.Fatal(err)
	}
	resetToken := accountOutboxToken(t, env.owner, "password-reset", "user@example.test")

	lock, err := env.owner.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(t.Context())
	if _, err := lock.Exec(t.Context(), `SELECT id FROM app.users WHERE email='user@example.test' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	resetDone := make(chan error, 1)
	go func() { resetDone <- service.SetPassword(t.Context(), env.cfg, resetToken, "reset-password") }()
	waitForAccountLock(t, env.owner, `query LIKE '%FROM app.users WHERE id=$1 FOR UPDATE%'`)
	loginDone := make(chan error, 1)
	go func() {
		_, _, loginErr := service.Login(t.Context(), "user@example.test", "user-password")
		loginDone <- loginErr
	}()
	waitForAccountLock(t, env.owner, `query LIKE '%FROM app.users WHERE email=$1 FOR UPDATE%'`)

	if err := lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-resetDone; err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !errors.Is(<-loginDone, auth.ErrInvalidCredentials) {
		t.Fatal("login with pre-reset password was accepted")
	}
	if _, err := service.Session(t.Context(), oldToken); err == nil {
		t.Fatal("reset retained the old session")
	}

	scheduler := rolePool(t, env, "ddp_scheduler")
	defer scheduler.Close()
	if _, err := scheduler.Exec(t.Context(), `SELECT ddp.expire_password_tokens()`); err != nil {
		t.Fatalf("scheduler cannot execute token cleanup: %v", err)
	}
	readonly := rolePool(t, env, "ddp_readonly")
	defer readonly.Close()
	for _, pool := range []*pgxpool.Pool{readonly, scheduler} {
		_, err := pool.Exec(t.Context(), `SELECT token_hash FROM app.password_tokens`)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatalf("authentication table refusal: %v", err)
		}
	}
	api := envServerAPI(t, env)
	_, err = api.Exec(t.Context(), `SELECT context FROM ops.outbox`)
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "42501" {
		t.Fatalf("private outbox refusal: %v", err)
	}
}

func envServerAPI(t *testing.T, env accountEnv) *pgxpool.Pool {
	t.Helper()
	cfg := env.owner.Config()
	cfg.ConnConfig.RuntimeParams["role"] = "ddp_api"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func rolePool(t *testing.T, env accountEnv, role string) *pgxpool.Pool {
	t.Helper()
	cfg := env.owner.Config()
	cfg.ConnConfig.RuntimeParams["role"] = role
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func waitForAccountLock(t *testing.T, pool *pgxpool.Pool, predicate string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND `+predicate).Scan(&waiting); err == nil && waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for account row lock (%s)", predicate)
}
