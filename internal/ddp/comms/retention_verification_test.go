package comms

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/cleanup"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestRetentionPurgeKeepsOutboxIdentityAndMasksContent(t *testing.T) {
	pool := commsDB(t)
	settings := fixtureSettings(newSMTPFixture(t))
	cfg := config.Comms{SMTP: &settings}
	id := enqueueTest(t, pool, cfg, Input{EffectKey: "retention/purge", Template: "x", Recipients: []string{"owner@example.test"}, Context: map[string]any{"name": "Ada"}})
	if _, err := pool.Exec(t.Context(), `UPDATE ops.outbox SET status='failed',finished_at=clock_timestamp()-interval '2 hours',rendered_at=clock_timestamp(),subject='subject',text_body='body',html_body='<p>body</p>' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var before struct {
		EffectKey, Template, Sender string
		Hash                        []byte
		Recipients                  []string
	}
	if err := pool.QueryRow(t.Context(), `SELECT effect_key,template,sender,payload_hash,recipients FROM ops.outbox WHERE id=$1`, id).Scan(&before.EffectKey, &before.Template, &before.Sender, &before.Hash, &before.Recipients); err != nil {
		t.Fatal(err)
	}
	_, err := cleanup.Run(t.Context(), pool, &config.Config{Retention: &config.Retention{RunLogs: "1h", Health: "1h", Messages: "1h"}})
	if err != nil {
		t.Fatal(err)
	}
	var after struct {
		EffectKey, Template, Sender string
		Hash                        []byte
		Recipients                  []string
		Context                     []byte
		Subject, Text, HTML         *string
		Expired                     *time.Time
	}
	if err := pool.QueryRow(t.Context(), `SELECT effect_key,template,sender,payload_hash,recipients,context,subject,text_body,html_body,content_expired_at FROM ops.outbox WHERE id=$1`, id).Scan(&after.EffectKey, &after.Template, &after.Sender, &after.Hash, &after.Recipients, &after.Context, &after.Subject, &after.Text, &after.HTML, &after.Expired); err != nil {
		t.Fatal(err)
	}
	if after.EffectKey != before.EffectKey || after.Template != before.Template || after.Sender != before.Sender || string(after.Hash) != string(before.Hash) || len(after.Recipients) != len(before.Recipients) || after.Recipients[0] != before.Recipients[0] {
		t.Fatalf("purge changed identity: before=%+v after=%+v", before, after)
	}
	if string(after.Context) != "{}" || after.Subject != nil || after.Text != nil || after.HTML != nil || after.Expired == nil {
		t.Fatalf("purge did not mask content: %+v", after)
	}
}

func TestExpiredActionsRefuseWithoutMutationOrAudit(t *testing.T) {
	pool := commsDB(t)
	settings := fixtureSettings(newSMTPFixture(t))
	cfg := config.Comms{SMTP: &settings}
	failed := enqueueTest(t, pool, cfg, Input{EffectKey: "retention/refused-failed", Template: "x", Recipients: []string{"owner@example.test"}})
	delivered := enqueueTest(t, pool, cfg, Input{EffectKey: "retention/refused-delivered", Template: "x", Recipients: []string{"owner@example.test"}})
	if _, err := pool.Exec(t.Context(), `UPDATE ops.outbox SET status=CASE WHEN id=$1 THEN 'failed' ELSE 'delivered' END,finished_at=clock_timestamp(),content_expired_at=clock_timestamp(),context='{}',rendered_at=NULL,subject=NULL,text_body=NULL,html_body=NULL WHERE id IN ($1,$2)`, failed, delivered); err != nil {
		t.Fatal(err)
	}
	var auditBefore int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ddp.audit WHERE action IN ('comms.retry','comms.resend')`).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	if err := Retry(t.Context(), pool, "message/"+failed); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("expired retry: %v", err)
	}
	if _, err := Resend(t.Context(), pool, "message/"+delivered, "retention/refused-copy"); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("expired resend: %v", err)
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM ops.outbox WHERE id=$1`, failed).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("expired retry mutated status to %q", status)
	}
	var n, auditAfter int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE effect_key='retention/refused-copy'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ddp.audit WHERE action IN ('comms.retry','comms.resend')`).Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	if n != 0 || auditAfter != auditBefore {
		t.Fatalf("expired actions mutated rows/audit: rows=%d audit %d->%d", n, auditBefore, auditAfter)
	}
}

func TestResendRefusesContentExpiredWhileWaiting(t *testing.T) {
	pool := commsDB(t)
	settings := fixtureSettings(newSMTPFixture(t))
	cfg := config.Comms{SMTP: &settings}
	id := enqueueTest(t, pool, cfg, Input{EffectKey: "retention/race", Template: "x", Recipients: []string{"owner@example.test"}})
	if _, err := pool.Exec(t.Context(), `UPDATE ops.outbox SET status='delivered',finished_at=clock_timestamp()-interval '2 hours',rendered_at=clock_timestamp(),subject='subject',text_body='body',html_body='<p>body</p>' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	lock, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(t.Context(), `SELECT id FROM ops.outbox WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	resendDone := make(chan struct{})
	var newID string
	var resendErr error
	go func() {
		newID, resendErr = Resend(context.Background(), pool, "message/"+id, "retention/race-copy")
		close(resendDone)
	}()
	select {
	case <-resendDone:
		t.Fatal("resend did not wait for original row lock")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := lock.Exec(t.Context(), `UPDATE ops.outbox SET context='{}',subject=NULL,text_body=NULL,html_body=NULL,content_expired_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-resendDone
	if !errors.Is(resendErr, audit.ErrRefused) || newID != "" {
		t.Fatalf("resend after expiry id=%q err=%v", newID, resendErr)
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE effect_key='retention/race-copy'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("resend copied expired content")
	}
}
