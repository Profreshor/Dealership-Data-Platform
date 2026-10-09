package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
)

func TestBackfillPlanCountsInclusiveDSTAndDownstream(t *testing.T) {
	cfg := &config.Config{
		Ddp: config.Identity{Timezone: "America/New_York"},
		Jobs: map[string]config.Job{
			"root": {Action: "ingest", Schedule: "30 1 * * *"},
			"send": {Action: "notify", After: []string{"job/root"}, Idempotency: &config.Idempotency{Strategy: "natural_key"}},
		},
	}
	from := time.Date(2025, 11, 2, 1, 30, 0, 0, time.UTC)
	through := time.Date(2025, 11, 3, 6, 30, 0, 0, time.UTC)
	plan, err := BackfillPlan(cfg, "job/root", from, through, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Occurrences != 2 || plan.Executions != 4 || len(plan.Strategies) != 1 || plan.Strategies[0] != "natural_key" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if err := ConfirmStrategies(cfg, plan.Jobs, []string{"natural_key"}); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillPlanRejectsInvalidRangeAndRequiresSchedule(t *testing.T) {
	cfg := &config.Config{Ddp: config.Identity{Timezone: "UTC"}, Jobs: map[string]config.Job{"manual": {Action: "check"}}}
	from := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	through := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := BackfillPlan(cfg, "job/manual", from, through, false); err == nil {
		t.Fatal("reversed range accepted")
	}
	through = time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := BackfillPlan(cfg, "job/manual", from, through, false); err == nil {
		t.Fatal("unscheduled root accepted")
	}
}

func TestBackfillPlanIncludesExactEndpointAndDoesNotExpandByDefault(t *testing.T) {
	cfg := &config.Config{Ddp: config.Identity{Timezone: "UTC"}, Jobs: map[string]config.Job{
		"root":  {Action: "check", Schedule: "0 0 * * *"},
		"child": {Action: "check", After: []string{"job/root"}},
	}}
	point := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	plan, err := BackfillPlan(cfg, "job/root", point, point, false)
	if err != nil || plan.Occurrences != 1 || plan.Executions != 1 || len(plan.Jobs) != 1 || len(plan.Strategies) != 0 {
		t.Fatalf("got plan=%+v err=%v", plan, err)
	}
}

func TestBackfillPlanSkipsSpringDSTGap(t *testing.T) {
	cfg := &config.Config{Ddp: config.Identity{Timezone: "America/New_York"}, Jobs: map[string]config.Job{
		"root": {Action: "check", Schedule: "30 2 * * *"},
	}}
	from := time.Date(2025, 3, 9, 0, 0, 0, 0, time.UTC)
	through := time.Date(2025, 3, 9, 8, 0, 0, 0, time.UTC)
	plan, err := BackfillPlan(cfg, "job/root", from, through, false)
	if err != nil || plan.Occurrences != 0 {
		t.Fatalf("got plan=%+v err=%v", plan, err)
	}
}

func TestBackfillPlanValidatesEndpointsAndReferences(t *testing.T) {
	cfg := &config.Config{Ddp: config.Identity{Timezone: "UTC"}, Jobs: map[string]config.Job{
		"root": {Action: "check", Schedule: "0 0 * * *"},
	}}
	valid := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		from, through time.Time
		ref           string
	}{
		{"zero from", time.Time{}, valid, "job/root"},
		{"zero through", valid, time.Time{}, "job/root"},
		{"future through", valid, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "job/root"},
		{"wrong ref", valid, valid, "job/missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BackfillPlan(cfg, tc.ref, tc.from, tc.through, false); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestSelectedJobsScheduledModel(t *testing.T) {
	cfg := &config.Config{Ddp: config.Identity{Timezone: "UTC"}, Models: map[string]config.Model{
		"daily": {Schedule: "0 0 * * *"},
	}}
	jobs, err := SelectedJobs(cfg, "model/daily", true)
	if err != nil || len(jobs) != 1 || jobs[0] != "model/daily" {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	if _, err := SelectedJobs(cfg, "model/missing", false); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestCommsRelayIsSingletonAndCannotBeBackfilled(t *testing.T) {
	cfg := &config.Config{Ddp: config.Identity{Timezone: "UTC"}}
	selected, err := SelectedJobs(cfg, jobs.CommsRelayRef, true)
	if err != nil || len(selected) != 1 || selected[0] != jobs.CommsRelayRef {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	if definition := jobs.Definitions(cfg)[jobs.CommsRelayRef]; definition.Schedule != "" || definition.Timeout != "50s" || definition.Catchup != "none" || definition.Retry.MaxAttempts != 1 {
		t.Fatalf("SMTP-disabled relay definition: %+v", definition)
	}
	cfg.Comms.SMTP = &config.SMTP{Addr: "localhost:2525", From: "ddp@example.com", TLS: "none"}
	if definition := jobs.Definitions(cfg)[jobs.CommsRelayRef]; definition.Schedule != "* * * * *" {
		t.Fatalf("SMTP-enabled relay schedule: %q", definition.Schedule)
	}
	point := time.Now().UTC().Add(-time.Minute)
	if _, err := BackfillPlan(cfg, jobs.CommsRelayRef, point, point, false); err == nil {
		t.Fatal("comms relay backfill accepted")
	}
	if _, err := Backfill(t.Context(), nil, cfg, jobs.CommsRelayRef, point, point, false, nil); err == nil {
		t.Fatal("comms relay backfill apply accepted")
	}
	if selected, err := SelectedJobs(cfg, jobs.HealthRef, true); err != nil || len(selected) != 1 || selected[0] != jobs.HealthRef {
		t.Fatalf("health selection: %v %v", selected, err)
	}
	if definition := jobs.Definitions(cfg)[jobs.HealthRef]; definition.Schedule != "* * * * *" || definition.Timeout != "50s" || definition.Catchup != "none" || definition.Retry.MaxAttempts != 1 {
		t.Fatalf("health definition: %+v", definition)
	}
	if _, err := BackfillPlan(cfg, jobs.HealthRef, point, point, false); err == nil {
		t.Fatal("health backfill accepted")
	}
}

func TestConfirmStrategiesRequiresEveryDistinctSideEffectStrategy(t *testing.T) {
	cfg := &config.Config{Ddp: config.Identity{Timezone: "UTC"}, Jobs: map[string]config.Job{
		"root":   {Action: "check", Schedule: "0 0 * * *"},
		"export": {Action: "export", After: []string{"job/root"}, Idempotency: &config.Idempotency{Strategy: "natural_key"}},
		"notify": {Action: "notify", After: []string{"job/export"}, Idempotency: &config.Idempotency{Strategy: "provider_key"}},
		"read":   {Action: "transform", After: []string{"job/root"}},
	}}
	refs := []string{"job/root", "job/export", "job/notify", "job/read"}
	if err := ConfirmStrategies(cfg, refs, []string{"natural_key"}); err == nil || !strings.Contains(err.Error(), "job/notify") {
		t.Fatalf("missing strategy accepted or wrong error: %v", err)
	}
	if err := ConfirmStrategies(cfg, refs, []string{"natural_key", "provider_key"}); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmStrategies(cfg, []string{"job/root"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmStrategies(cfg, []string{"job/missing"}, nil); err == nil {
		t.Fatal("unknown reference accepted")
	}
	cfg.Jobs["broken"] = config.Job{Action: "export"}
	if err := ConfirmStrategies(cfg, []string{"job/broken"}, nil); err == nil {
		t.Fatal("missing strategy accepted")
	}
}
