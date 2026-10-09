package comms

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func commsDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_comms_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
		_ = admin.Close(context.Background())
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(t.Context(), conn.Conn()); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	return pool
}
func commsRole(t *testing.T, pool *pgxpool.Pool, role string) *pgxpool.Pool {
	t.Helper()
	cfg := pool.Config()
	cfg.ConnConfig.RuntimeParams["role"] = role
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
func enqueueTest(t *testing.T, pool *pgxpool.Pool, cfg config.Comms, input Input) string {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	id, err := Enqueue(t.Context(), tx, cfg, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return id
}
func due(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE ops.outbox SET available_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxTransactionReplayPermissionsAndDelivery(t *testing.T) {
	pool := commsDB(t)
	producer := commsRole(t, pool, "ddp_job")
	scheduler := commsRole(t, pool, "ddp_scheduler")
	reader := commsRole(t, pool, "ddp_readonly")
	failedSMTP := newSMTPFixture(t)
	failedSMTP.rcptFail = true
	settings := fixtureSettings(failedSMTP)
	cfg := config.Comms{SMTP: &settings, Groups: map[string]config.Group{"owners": {Recipients: []string{"owner@example.test"}}}}
	root := writeTemplates(t, `{{define "subject"}}Hello {{.Name}}{{end}}{{define "body"}}Private {{.Name}}{{end}}`, "")
	input := Input{EffectKey: "customer-notice/one", Template: "x", Recipients: []string{"group/owners"}, Context: map[string]any{"Name": "Ada"}}
	tx, err := producer.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Enqueue(t.Context(), tx, cfg, input); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback left messages: %d %v", count, err)
	}
	id := enqueueTest(t, producer, cfg, input)
	if got := enqueueTest(t, producer, cfg, input); got != id {
		t.Fatal("replay created a second message")
	}
	tx, err = producer.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.Context = map[string]any{"Name": "other"}
	_, err = Enqueue(t.Context(), tx, cfg, changed)
	_ = tx.Rollback(t.Context())
	if !errors.Is(err, ErrEffectConflict) {
		t.Fatalf("effect conflict: %v", err)
	}
	if _, err = producer.Exec(t.Context(), `UPDATE ops.outbox SET status='delivered' WHERE id=$1`, id); err == nil {
		t.Fatal("producer forged delivery")
	}
	if _, err = producer.Exec(t.Context(), `SELECT text_body FROM ops.outbox`); err == nil {
		t.Fatal("producer read message bodies")
	}
	result, err := RelayOne(t.Context(), scheduler, cfg, root, "")
	if err != nil || result.Status != "pending" || result.Attempt != 1 {
		t.Fatalf("first delivery: %#v %v", result, err)
	}
	var subject, body string
	if err = pool.QueryRow(t.Context(), `SELECT subject,text_body FROM ops.outbox WHERE id=$1`, id).Scan(&subject, &body); err != nil || subject != "Hello Ada" || body != "Private Ada" {
		t.Fatalf("persisted rendering differs: %v", err)
	}
	detail, err := Show(t.Context(), reader, "message/"+id)
	if err != nil || len(detail.Deliveries) != 1 || detail.Deliveries[0].Status != "failed" {
		t.Fatalf("history: %#v %v", detail, err)
	}
	if detail.Message.AvailableAt.Before(time.Now().Add(20 * time.Second)) {
		t.Fatal("automatic retry was not delayed")
	}
	records, err := List(t.Context(), reader)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(records)
	if strings.Contains(string(metadata), "Ada") || strings.Contains(string(metadata), "owner@example") {
		t.Fatal("inspection exposed content")
	}
	if _, err = reader.Exec(t.Context(), `SELECT context FROM ops.outbox`); err == nil {
		t.Fatal("reader accessed private message context")
	}
	if err = Retry(t.Context(), reader, "message/"+id); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("readonly retry: %v", err)
	}
	if err = os.WriteFile(filepath.Join(root, "templates/comms/x.txt"), []byte("broken template"), 0600); err != nil {
		t.Fatal(err)
	}
	goodSMTP := newSMTPFixture(t)
	settings.Addr = goodSMTP.listener.Addr().String()
	due(t, pool, id)
	result, err = RelayOne(t.Context(), scheduler, cfg, root, "")
	if err != nil || result.Status != "delivered" || result.Attempt != 2 {
		t.Fatalf("saved-content retry: %#v %v", result, err)
	}
	if err = Retry(t.Context(), scheduler, "message/"+id); err == nil {
		t.Fatal("delivered message retried")
	}
	newID, err := Resend(t.Context(), scheduler, "message/"+id, "manual-resend/one")
	if err != nil || newID == id {
		t.Fatalf("resend: %s %v", newID, err)
	}
	replay, err := Resend(t.Context(), scheduler, "message/"+id, "manual-resend/one")
	if err != nil || replay != newID {
		t.Fatalf("resend replay: %s %v", replay, err)
	}
	result, err = RelayOne(t.Context(), scheduler, cfg, root, "message/"+newID)
	if err != nil || result.Status != "delivered" {
		t.Fatalf("resend saved content: %#v %v", result, err)
	}
	if _, err = Resend(t.Context(), reader, "message/"+id, "forbidden"); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("readonly resend: %v", err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM ddp.audit WHERE action='comms.resend'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("resend audit: %d %v", count, err)
	}
}

func TestOutboxConcurrentEffectAndInterruptedRecovery(t *testing.T) {
	pool := commsDB(t)
	smtp := newSMTPFixture(t)
	settings := fixtureSettings(smtp)
	cfg := config.Comms{SMTP: &settings}
	root := writeTemplates(t, `{{define "subject"}}saved{{end}}{{define "body"}}content{{end}}`, "")
	input := Input{EffectKey: "same", Template: "x", Recipients: []string{"owner@example.test"}}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			tx, err := pool.Begin(t.Context())
			if err != nil {
				errs <- err
				return
			}
			defer tx.Rollback(t.Context())
			id, err := Enqueue(t.Context(), tx, cfg, input)
			if err == nil {
				err = tx.Commit(t.Context())
			}
			if err != nil {
				errs <- err
			} else {
				ids <- id
			}
		})
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	id := ""
	for got := range ids {
		if id != "" && got != id {
			t.Fatal("concurrent duplicate effect")
		}
		id = got
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ops.outbox SET status='delivering',attempts=1,rendered_at=clock_timestamp(),subject='saved',text_body='content',html_body='' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO ops.deliveries(id,message_id,number,status) VALUES($1,$2,1,'delivering')`, ulid.Make().String(), id); err != nil {
		t.Fatal(err)
	}
	lock, err := pgx.ConnectConfig(t.Context(), pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close(t.Context())
	if _, err = lock.Exec(t.Context(), `SELECT pg_advisory_lock(hashtextextended('ddp:comms_relay',0))`); err != nil {
		t.Fatal(err)
	}
	result, err := RelayOne(t.Context(), pool, cfg, root, "")
	if err != nil || result.Status != "busy" {
		t.Fatalf("active relay: %#v %v", result, err)
	}
	_ = lock.Close(t.Context())
	result, err = RelayOne(t.Context(), pool, cfg, root, "")
	if err != nil || result.Status != "idle" {
		t.Fatalf("recovery delay: %#v %v", result, err)
	}
	detail, err := Show(t.Context(), pool, "message/"+id)
	if err != nil || detail.Deliveries[0].Status != "interrupted" || detail.Message.Status != "pending" {
		t.Fatalf("recovered state: %#v %v", detail, err)
	}
	due(t, pool, id)
	result, err = RelayOne(t.Context(), pool, cfg, root, "")
	if err != nil || result.Status != "delivered" || result.Attempt != 2 {
		t.Fatalf("recovered delivery: %#v %v", result, err)
	}
}

func TestOutboxRenderFailureAndManualRetry(t *testing.T) {
	pool := commsDB(t)
	smtp := newSMTPFixture(t)
	settings := fixtureSettings(smtp)
	cfg := config.Comms{SMTP: &settings}
	root := writeTemplates(t, `{{define "subject"}}x{{end}}{{define "body"}}{{.Missing}}{{end}}`, "")
	id := enqueueTest(t, pool, cfg, Input{EffectKey: "render-failure", Template: "x", Recipients: []string{"owner@example.test"}})
	result, err := RelayOne(t.Context(), pool, cfg, root, "")
	if err != nil || result.Status != "failed" {
		t.Fatalf("render failure: %#v %v", result, err)
	}
	if err = os.WriteFile(filepath.Join(root, "templates/comms/x.txt"), []byte(`{{define "subject"}}fixed{{end}}{{define "body"}}fixed{{end}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Retry(t.Context(), pool, "message/"+id); err != nil {
		t.Fatal(err)
	}
	result, err = RelayOne(t.Context(), pool, cfg, root, "")
	if err != nil || result.Status != "delivered" || result.Attempt != 2 {
		t.Fatalf("manual retry: %#v %v", result, err)
	}
}
