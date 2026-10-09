package comms

import (
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestOutboxAutomaticBudgetAndManualRetryExtension(t *testing.T) {
	pool := commsDB(t)
	for attempt, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute, 5: 8 * time.Minute, 20: time.Hour} {
		if got := retryDelay(attempt); got != want {
			t.Fatalf("retry delay %d = %s, want %s", attempt, got, want)
		}
	}
	failed := newSMTPFixture(t)
	failed.rcptFail = true
	settings := fixtureSettings(failed)
	cfg := config.Comms{SMTP: &settings}
	root := writeTemplates(t, `{{define "subject"}}subject{{end}}{{define "body"}}body{{end}}`, "")
	id := enqueueTest(t, pool, cfg, Input{EffectKey: "automatic-budget", Template: "x", Recipients: []string{"owner@example.test"}})

	wantDelays := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for attempt, wantDelay := range wantDelays {
		due(t, pool, id)
		before := time.Now()
		result, err := RelayOne(t.Context(), pool, cfg, root, "")
		if err != nil || result.Attempt != attempt+1 {
			t.Fatalf("automatic attempt %d: %#v %v", attempt+1, result, err)
		}
		wantStatus := "pending"
		if attempt == len(wantDelays)-1 {
			wantStatus = "failed"
		}
		if result.Status != wantStatus {
			t.Fatalf("automatic attempt %d status = %q, want %q", attempt+1, result.Status, wantStatus)
		}
		var status string
		var available time.Time
		if err = pool.QueryRow(t.Context(), `SELECT status,available_at FROM ops.outbox WHERE id=$1`, id).Scan(&status, &available); err != nil {
			t.Fatal(err)
		}
		if status != wantStatus || available.Before(before.Add(wantDelay-time.Second)) {
			t.Fatalf("automatic attempt %d state = %q at %s, want %q after %s", attempt+1, status, available, wantStatus, wantDelay)
		}
	}

	var attempts, maxAttempts int
	if err := pool.QueryRow(t.Context(), `SELECT attempts,max_attempts FROM ops.outbox WHERE id=$1`, id).Scan(&attempts, &maxAttempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 5 || maxAttempts != 5 {
		t.Fatalf("exhausted budget = attempts %d/max %d, want 5/5", attempts, maxAttempts)
	}
	if err := Retry(t.Context(), pool, "message/"+id); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status,max_attempts FROM ops.outbox WHERE id=$1`, id).Scan(&status, &maxAttempts); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || maxAttempts != 10 {
		t.Fatalf("manual retry state = %q/max %d, want pending/10", status, maxAttempts)
	}

	good := newSMTPFixture(t)
	settings.Addr = good.listener.Addr().String()
	due(t, pool, id)
	result, err := RelayOne(t.Context(), pool, cfg, root, "")
	if err != nil || result.Status != "delivered" || result.Attempt != 6 {
		t.Fatalf("manual retry delivery: %#v %v", result, err)
	}
}
