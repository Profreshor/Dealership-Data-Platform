package health

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestHealthAdversarialRetentionRoutingAndDeferredDelivery(t *testing.T) {
	pool := healthDB(t)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	base := time.Now().UTC().Truncate(time.Microsecond)
	o := Observation{Ref: "health/adversarial", Target: "table/core.example", Severity: "critical", Message: "synthetic", Notify: []string{"group/owners"}}
	for i, state := range []string{"ok", "failing", "failing", "failing", "unknown", "ok"} {
		o.State = state
		if _, err := record(t.Context(), conn.Conn(), o, base.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	var states int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM ops.health_evaluations WHERE rule_ref=$1 AND state IN ('ok','failing','unknown')`, o.Ref).Scan(&states); err != nil {
		t.Fatal(err)
	}
	if states != 6 {
		t.Fatalf("retained states=%d, want 6", states)
	}
	var alerts int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM ops.alerts WHERE rule_ref=$1`, o.Ref).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts != 5 { // initial alert, two daily critical reminders, unknown transition, recovery
		t.Fatalf("alerts=%d, want 5", alerts)
	}
	var routed []string
	if err := conn.QueryRow(t.Context(), `SELECT recipients FROM ops.alerts WHERE rule_ref=$1 AND kind='alert' ORDER BY created_at DESC LIMIT 1`, o.Ref).Scan(&routed); err != nil {
		t.Fatal(err)
	}
	if len(routed) != 1 || routed[0] != "group/platform_ops" {
		t.Fatalf("unknown routing=%v", routed)
	}
	var recovered []string
	if err := conn.QueryRow(t.Context(), `SELECT recipients FROM ops.alerts WHERE rule_ref=$1 AND kind='recovery'`, o.Ref).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 2 || recovered[0] != "group/owners" || recovered[1] != "group/platform_ops" {
		t.Fatalf("recovery routing=%v", recovered)
	}

	deferred := Observation{Ref: "health/missing-group", State: "failing", Severity: "warning", Message: "synthetic", Notify: []string{"group/later"}}
	if _, err := record(t.Context(), conn.Conn(), deferred, base); err != nil {
		t.Fatal(err)
	}
	if err := queueAlerts(t.Context(), conn.Conn(), config.Comms{}); err != nil {
		t.Fatal(err)
	}
	var alertID string
	var messageID *string
	var notificationError *string
	if err := conn.QueryRow(t.Context(), `SELECT id,message_id,notification_error FROM ops.alerts WHERE rule_ref=$1`, deferred.Ref).Scan(&alertID, &messageID, &notificationError); err != nil {
		t.Fatal(err)
	}
	if messageID != nil || notificationError == nil {
		t.Fatalf("deferred alert message_id=%v error=%v", messageID, notificationError)
	}
	valid := config.Comms{SMTP: &config.SMTP{Addr: "127.0.0.1:1", From: "sender@example.test", TLS: "none"}, Groups: map[string]config.Group{"later": {Recipients: []string{"later@example.test"}}}}
	if err := queueAlerts(t.Context(), conn.Conn(), valid); err != nil {
		t.Fatal(err)
	}
	if err := queueAlerts(t.Context(), conn.Conn(), valid); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(t.Context(), `SELECT message_id,notification_error FROM ops.alerts WHERE id=$1`, alertID).Scan(&messageID, &notificationError); err != nil {
		t.Fatal(err)
	}
	if messageID == nil || notificationError != nil {
		t.Fatalf("later delivery message_id=%v error=%v", messageID, notificationError)
	}
	var outbox int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE effect_key='health/' || $1`, alertID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if outbox != 1 {
		t.Fatalf("deferred outbox rows=%d, want 1", outbox)
	}
}

func TestHealthQueueConcurrentDeduplicatesPersistedAlert(t *testing.T) {
	pool := healthDB(t)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	o := Observation{Ref: "health/concurrent", State: "failing", Severity: "critical", Message: "synthetic", Notify: []string{"group/ops"}}
	if _, err := record(t.Context(), conn.Conn(), o, time.Now().UTC()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	cfg := config.Comms{SMTP: &config.SMTP{Addr: "127.0.0.1:1", From: "sender@example.test", TLS: "none"}, Groups: map[string]config.Group{"ops": {Recipients: []string{"ops@example.test"}}}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := pool.Acquire(context.Background())
			if err != nil {
				errs <- err
				return
			}
			defer c.Release()
			errs <- queueAlerts(context.Background(), c.Conn(), cfg)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var alertID string
	if err := pool.QueryRow(t.Context(), `SELECT id FROM ops.alerts WHERE rule_ref=$1`, o.Ref).Scan(&alertID); err != nil {
		t.Fatal(err)
	}
	var outbox, linked int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox WHERE effect_key='health/' || $1`, alertID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.alerts WHERE rule_ref=$1 AND message_id IS NOT NULL`, o.Ref).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if outbox != 1 || linked != 1 {
		t.Fatalf("concurrent queue outbox=%d linked_alerts=%d", outbox, linked)
	}
}

func TestHealthEvaluateCanceledContextDoesNotKeepLock(t *testing.T) {
	pool := healthDB(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	cfg := &config.Config{}
	if _, err := Evaluate(canceled, pool, cfg, t.TempDir()); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled evaluation err=%v", err)
	}
	if _, err := Evaluate(t.Context(), pool, cfg, t.TempDir()); err != nil {
		t.Fatalf("lock remained after cancellation: %v", err)
	}
}

func TestHealthEvaluationRejectsSecondConcurrentRun(t *testing.T) {
	pool := healthDB(t)
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(t.Context(), `SELECT pg_try_advisory_lock(hashtextextended('ddp:health',0))`).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("test could not acquire health lock")
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('ddp:health',0))`)
	if _, err := Evaluate(t.Context(), pool, &config.Config{}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second evaluation err=%v", err)
	}
}
