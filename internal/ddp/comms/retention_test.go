package comms

import (
	"errors"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestExpiredMessageGuardsAndEffectIdentity(t *testing.T) {
	pool := commsDB(t)
	settings := fixtureSettings(newSMTPFixture(t))
	cfg := config.Comms{SMTP: &settings}
	input := Input{EffectKey: "retention/effect", Template: "x", Recipients: []string{"owner@example.test"}, Context: map[string]any{"name": "Ada"}}
	id := enqueueTest(t, pool, cfg, input)
	if _, err := pool.Exec(t.Context(), `UPDATE ops.outbox SET status='failed',finished_at=clock_timestamp(),content_expired_at=clock_timestamp(),context='{}',rendered_at=NULL,subject=NULL,text_body=NULL,html_body=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := Retry(t.Context(), pool, "message/"+id); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("expired retry: %v", err)
	}
	if got := enqueueTest(t, pool, cfg, input); got != id {
		t.Fatalf("expired replay id=%q want %q", got, id)
	}
	changed := input
	changed.Context = map[string]any{"name": "Grace"}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Enqueue(t.Context(), tx, cfg, changed)
	_ = tx.Rollback(t.Context())
	if !errors.Is(err, ErrEffectConflict) {
		t.Fatalf("expired changed payload: %v", err)
	}

	resend := enqueueTest(t, pool, cfg, Input{EffectKey: "retention/resend", Template: "x", Recipients: input.Recipients})
	if _, err := pool.Exec(t.Context(), `UPDATE ops.outbox SET status='delivered',finished_at=clock_timestamp(),content_expired_at=clock_timestamp(),context='{}',rendered_at=NULL,subject=NULL,text_body=NULL,html_body=NULL WHERE id=$1`, resend); err != nil {
		t.Fatal(err)
	}
	if _, err := Resend(t.Context(), pool, "message/"+resend, "retention/resend-copy"); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("expired resend: %v", err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE effect_key='retention/resend-copy'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired resend inserted row: %d %v", count, err)
	}
}
