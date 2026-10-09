package scheduler

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/schedule"
)

// BackfillPreview describes the work a historical backfill would create.
type BackfillPreview struct {
	JobRef      string    `json:"job_ref"`
	From        time.Time `json:"from"`
	Through     time.Time `json:"through"`
	Timezone    string    `json:"timezone"`
	Jobs        []string  `json:"jobs"`
	Occurrences int64     `json:"occurrences"`
	Executions  int64     `json:"executions"`
	Strategies  []string  `json:"strategies"`
}

// SelectedJobs resolves a root reference, optionally expanding its downstream chain.
func SelectedJobs(cfg *config.Config, ref string, downstream bool) ([]string, error) {
	definitions, err := definitionsForBackfill(cfg)
	if err != nil {
		return nil, err
	}
	if _, ok := definitions[ref]; !ok {
		return nil, fmt.Errorf("backfill: unknown job %q", ref)
	}
	if !downstream || !strings.HasPrefix(ref, "job/") {
		return []string{ref}, nil
	}
	return Chain(cfg, ref)
}

// ConfirmStrategies checks the confirmations required by selected side-effecting jobs.
func ConfirmStrategies(cfg *config.Config, refs []string, confirmed []string) error {
	definitions, err := definitionsForBackfill(cfg)
	if err != nil {
		return err
	}
	accepted := make(map[string]struct{}, len(confirmed))
	for _, strategy := range confirmed {
		accepted[strategy] = struct{}{}
	}
	for _, ref := range refs {
		job, ok := definitions[ref]
		if !ok {
			return fmt.Errorf("backfill: unknown job %q", ref)
		}
		if !job.SideEffecting() {
			continue
		}
		if err := job.ValidateIdempotency(); err != nil {
			return fmt.Errorf("%s: %w", ref, err)
		}
		strategy := job.Idempotency.Strategy
		if _, ok := accepted[strategy]; !ok {
			return fmt.Errorf("%s requires confirmation of idempotency strategy %q", ref, strategy)
		}
	}
	return nil
}

// BackfillPlan counts scheduled occurrences without connecting to a database.
func BackfillPlan(cfg *config.Config, ref string, from, through time.Time, downstream bool) (BackfillPreview, error) {
	var preview BackfillPreview
	if cfg == nil {
		return preview, fmt.Errorf("backfill: nil config")
	}
	if jobs.IsSystem(ref) {
		return preview, fmt.Errorf("%w: %s cannot be backfilled", audit.ErrRefused, ref)
	}
	if from.IsZero() || through.IsZero() {
		return preview, fmt.Errorf("backfill: from and through must be non-zero")
	}
	if through.Before(from) {
		return preview, fmt.Errorf("backfill: through precedes from")
	}
	now := time.Now().UTC()
	if through.UTC().After(now) {
		return preview, fmt.Errorf("backfill: through must not be in the future")
	}
	definitions, err := definitionsForBackfill(cfg)
	if err != nil {
		return preview, err
	}
	root, ok := definitions[ref]
	if !ok {
		return preview, fmt.Errorf("backfill: unknown job %q", ref)
	}
	if root.Schedule == "" {
		return preview, fmt.Errorf("backfill: root %q has no schedule", ref)
	}
	jobs, err := SelectedJobs(cfg, ref, downstream)
	if err != nil {
		return preview, err
	}
	strategies := make(map[string]struct{})
	for _, jobRef := range jobs {
		job := definitions[jobRef]
		if job.SideEffecting() {
			if err := job.ValidateIdempotency(); err != nil {
				return preview, fmt.Errorf("backfill %s: %w", jobRef, err)
			}
			strategies[job.Idempotency.Strategy] = struct{}{}
		}
	}
	preview = BackfillPreview{JobRef: ref, From: from, Through: through, Timezone: cfg.Ddp.Timezone, Jobs: jobs, Strategies: []string{}}
	for strategy := range strategies {
		preview.Strategies = append(preview.Strategies, strategy)
	}
	slices.Sort(preview.Strategies)
	// Each is exclusive at its start; subtracting one nanosecond makes from inclusive.
	err = schedule.Each(root.Schedule, cfg.Ddp.Timezone, from.Add(-time.Nanosecond), through, func(schedule.Occurrence) error {
		preview.Occurrences++
		return nil
	})
	if err != nil {
		return BackfillPreview{}, fmt.Errorf("backfill %s: %w", ref, err)
	}
	preview.Executions = preview.Occurrences * int64(len(jobs))
	return preview, nil
}

func definitionsForBackfill(cfg *config.Config) (map[string]config.Job, error) {
	if cfg == nil {
		return nil, fmt.Errorf("backfill: nil config")
	}
	return jobs.Definitions(cfg), nil
}
