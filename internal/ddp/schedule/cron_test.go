package schedule

import (
	"testing"
	"time"
)

func TestBetweenBoundariesAndUTC(t *testing.T) {
	a := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	got, err := Between("0 * * * *", "UTC", a, b)
	if err != nil || len(got) != 2 || !got[0].At.Equal(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)) || got[1].Local != "2026-01-01T02:00" {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestBetweenDST(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	springStart := time.Date(2026, 3, 8, 0, 0, 0, 0, loc).UTC()
	springEnd := time.Date(2026, 3, 9, 0, 0, 0, 0, loc).UTC()
	got, err := Between("30 2 * * *", "America/New_York", springStart, springEnd)
	if err != nil || len(got) != 0 {
		t.Fatalf("spring got %#v, %v", got, err)
	}

	fallStart := time.Date(2026, 11, 1, 0, 0, 0, 0, loc).UTC()
	fallEnd := time.Date(2026, 11, 2, 0, 0, 0, 0, loc).UTC()
	got, err = Between("30 1 * * *", "America/New_York", fallStart, fallEnd)
	if err != nil || len(got) != 1 || got[0].Local != "2026-11-01T01:30" {
		t.Fatalf("fall got %#v, %v", got, err)
	}
	first, err := Between("30 1 * * *", "America/New_York", fallStart, time.Date(2026, 11, 1, 5, 45, 0, 0, time.UTC))
	if err != nil || len(first) != 1 {
		t.Fatalf("fall first split got %#v, %v", first, err)
	}
	second, err := Between("30 1 * * *", "America/New_York", time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC), fallEnd)
	if err != nil || len(second) != 1 {
		t.Fatalf("fall second split got %#v, %v", second, err)
	}
}

func TestParseRejectsInvalidForms(t *testing.T) {
	for _, expression := range []string{"@daily", "0 0 1 1 * *", "CRON_TZ=UTC 0 0 * * *"} {
		if _, err := Parse(expression, "UTC"); err == nil {
			t.Errorf("Parse(%q) succeeded", expression)
		}
	}
	if _, err := Parse("0 0 * * *", "Mars/Olympus"); err == nil {
		t.Error("invalid timezone succeeded")
	}
	got, err := Between("0 0 31 2 *", "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(got) != 0 {
		t.Fatalf("impossible date got %#v, %v", got, err)
	}
}

func TestBetweenORAndLongOutage(t *testing.T) {
	got, err := Between("0 0 1 * MON", "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(got) != 10 { // first of month plus every Monday (robfig's DOM/DOW OR)
		t.Fatalf("OR got %d, %v", len(got), err)
	}
	got, err = Between("* * * * *", "UTC", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(got) != 31*24*60 {
		t.Fatalf("long outage: %d %v", len(got), err)
	}
}

func TestBetweenRejectsZeroAndReversed(t *testing.T) {
	date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := Between("0 0 * * *", "UTC", time.Time{}, date); err == nil {
		t.Error("zero after succeeded")
	}
	if _, err := Between("0 0 * * *", "UTC", date, date.Add(-time.Minute)); err == nil {
		t.Error("reversed range succeeded")
	}
}
