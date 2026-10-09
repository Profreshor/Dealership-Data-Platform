package serving

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccountLinkLifetimeAndAtomicityAgainstPostgres(t *testing.T) {
	env := accountTestEnv(t)
	cfg := env.owner.Config()
	cfg.ConnConfig.RuntimeParams["role"] = "ddp_api"
	api, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	service := auth.New(api, time.Hour)
	invite := func(email string) string {
		t.Helper()
		if _, err := service.Invite(t.Context(), env.cfg, email, []string{"reader"}, ""); err != nil {
			t.Fatal(err)
		}
		return accountOutboxToken(t, env.owner, "welcome", email)
	}
	old := invite("pending@example.test")
	if _, _, err := service.Login(t.Context(), "pending@example.test", "ddp-dummy-password"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("pending account logged in: %v", err)
	}
	current := invite("pending@example.test")
	if current == old {
		t.Fatal("reissue reused token")
	}
	if err := service.SetPassword(t.Context(), env.cfg, old, "first-password"); !errors.Is(err, auth.ErrInvalidLink) {
		t.Fatalf("old link accepted: %v", err)
	}
	var stored []byte
	if err := env.owner.QueryRow(t.Context(), `SELECT token_hash FROM app.password_tokens WHERE user_id=(SELECT id FROM app.users WHERE email='pending@example.test')`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(auth.HashToken(current)) {
		t.Fatal("authentication state does not store token hash")
	}
	if _, err := env.owner.Exec(t.Context(), `UPDATE app.password_tokens SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE token_hash=$1`, stored); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPassword(t.Context(), env.cfg, current, "first-password"); !errors.Is(err, auth.ErrInvalidLink) {
		t.Fatalf("expired link accepted: %v", err)
	}
	var removed int
	if err := env.owner.QueryRow(t.Context(), `SELECT ddp.expire_password_tokens()`).Scan(&removed); err != nil || removed != 1 {
		t.Fatalf("expired cleanup=%d: %v", removed, err)
	}
	current = invite("pending@example.test")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- service.SetPassword(t.Context(), env.cfg, current, "first-password") })
	}
	wg.Wait()
	close(results)
	successes, invalid := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, auth.ErrInvalidLink) {
			invalid++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("concurrent claims success=%d invalid=%d", successes, invalid)
	}
	if _, err := service.Invite(t.Context(), env.cfg, "pending@example.test", nil, ""); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("active account reinvited: %v", err)
	}
	if _, err := service.Invite(t.Context(), env.cfg, "badrole@example.test", []string{"missing"}, ""); !errors.Is(err, auth.ErrInvalidAccount) {
		t.Fatalf("unknown role: %v", err)
	}
	var n int
	if err := env.owner.QueryRow(t.Context(), `SELECT count(*) FROM app.users WHERE email='badrole@example.test'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("bad role committed user: %d %v", n, err)
	}

	// A failure at the last transactional write must preserve credentials, the
	// usable link and old sessions, and must not enqueue a false change notice.
	session, _, err := service.Login(t.Context(), "pending@example.test", "first-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RequestReset(t.Context(), env.cfg, "pending@example.test"); err != nil {
		t.Fatal(err)
	}
	reset := accountOutboxToken(t, env.owner, "password-reset", "pending@example.test")
	if _, err := env.owner.Exec(t.Context(), `CREATE FUNCTION app.reject_password_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='users.password_set' THEN RAISE EXCEPTION 'private failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_password_audit BEFORE INSERT ON ddp.audit FOR EACH ROW EXECUTE FUNCTION app.reject_password_audit()`); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPassword(t.Context(), env.cfg, reset, "second-password"); err == nil || strings.Contains(err.Error(), "private failure") {
		t.Fatalf("audit failure: %v", err)
	}
	if _, err := service.Session(t.Context(), session); err != nil {
		t.Fatalf("rolled back reset revoked session: %v", err)
	}
	if _, _, err := service.Login(t.Context(), "pending@example.test", "first-password"); err != nil {
		t.Fatalf("rolled back reset changed password: %v", err)
	}
	if err := env.owner.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE template='password-changed' AND recipients @> ARRAY['pending@example.test']::text[]`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("false notice: %d %v", n, err)
	}
	if _, err := env.owner.Exec(t.Context(), `DROP TRIGGER reject_password_audit ON ddp.audit`); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPassword(t.Context(), env.cfg, reset, "second-password"); err != nil {
		t.Fatalf("rollback consumed link: %v", err)
	}
	if _, err := service.Session(t.Context(), session); err == nil {
		t.Fatal("successful reset retained old session")
	}
}
