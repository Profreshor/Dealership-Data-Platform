package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/comms"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/inspect"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ponytail: detail wraps up to 100 saved attempts in memory; page attempts if large logs make navigation slow.
const projectionLimit = 100

// Load turns the existing inspection facts into the small records the TUI needs.
func Load(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, screen Screen) (Snapshot, error) {
	if pool == nil || cfg == nil {
		return Snapshot{}, errors.New("tui data requires pool and config")
	}
	var out Snapshot
	switch screen {
	case Overview, Failures:
		status, e := inspect.Overview(ctx, pool, cfg)
		if e != nil {
			return Snapshot{}, e
		}
		out.Summary = []string{fmt.Sprintf("scheduler: %s", status.Scheduler.State), "health: " + status.HealthObservation, fmt.Sprintf("outbox: pending=%d delivering=%d failed=%d", status.Outbox["pending"], status.Outbox["delivering"], status.Outbox["failed"])}
		out.Summary = append(out.Summary, fmt.Sprintf("recent failures: %d | saved health checks: %d", len(status.RecentFailures), len(status.Health)))
		if status.PendingAlerts > 0 {
			out.Summary = append(out.Summary, fmt.Sprintf("pending alerts: %d", status.PendingAlerts))
		}
		if screen == Failures {
			for _, run := range status.RecentFailures {
				out.Rows = append(out.Rows, runRow(run))
			}
		} else {
			for _, job := range status.Jobs {
				out.Rows = append(out.Rows, jobRow(job))
			}
		}
	case Jobs:
		jobs, e := inspect.ListJobs(ctx, pool, cfg)
		if e != nil {
			return Snapshot{}, e
		}
		for _, job := range jobs {
			out.Rows = append(out.Rows, jobRow(job))
		}
	case Runs:
		runs, e := inspect.ListRuns(ctx, pool, "", projectionLimit)
		if e != nil {
			return Snapshot{}, e
		}
		for _, run := range runs {
			out.Rows = append(out.Rows, runRow(run))
		}
	case Logs:
		runs, e := inspect.ListRuns(ctx, pool, "", projectionLimit)
		if e != nil {
			return Snapshot{}, e
		}
		for _, run := range runs {
			out.Rows = append(out.Rows, runRow(run))
		}
	case Integrations:
		items, e := inspect.ListIntegrations(ctx, pool, cfg)
		if e != nil {
			return Snapshot{}, e
		}
		for _, item := range items {
			out.Rows = append(out.Rows, Row{Ref: item.Ref, Label: item.Kind, State: fmt.Sprintf("%d jobs, %d tables", len(item.Jobs), len(item.LandedTables))})
		}
	case Models:
		definitions := jobs.Definitions(cfg)
		names := make([]string, 0, len(cfg.Models))
		for name := range cfg.Models {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			model := cfg.Models[name]
			ref := "model/" + name
			jobRef := ""
			if _, ok := definitions[ref]; ok {
				jobRef = ref
			}
			out.Rows = append(out.Rows, Row{Ref: ref, Label: name, State: model.Materialization, JobRef: jobRef})
		}
	case Tables:
		items, e := inspect.ListTables(ctx, pool, cfg)
		if e != nil {
			return Snapshot{}, e
		}
		for _, item := range items {
			state := "missing"
			if item.Exists {
				state = fmt.Sprintf("%s, %d estimated rows", item.Kind, item.EstimatedRows)
			}
			out.Rows = append(out.Rows, Row{Ref: item.Ref, Label: strings.TrimPrefix(item.Ref, "table/"), State: state})
		}
	case Health:
		items, e := health.Latest(ctx, pool)
		if e != nil {
			return Snapshot{}, e
		}
		for _, item := range items {
			out.Rows = append(out.Rows, Row{Ref: item.Ref, Label: item.Target + " observed " + utc(item.ObservedAt), State: item.State})
		}
	case Communications:
		items, e := comms.List(ctx, pool)
		if e != nil {
			return Snapshot{}, e
		}
		for _, item := range items {
			state := item.Status
			if item.ContentExpiredAt != nil {
				state += " expired"
			}
			out.Rows = append(out.Rows, Row{Ref: "message/" + item.ID, Label: item.Template, State: state})
		}
	default:
		return Snapshot{}, fmt.Errorf("unknown tui screen %q", screen)
	}
	return out, nil
}

func jobRow(job inspect.Job) Row {
	state := "declared"
	if job.Paused {
		state = "paused"
	} else if job.NewestRun != nil {
		state = job.NewestRun.Status
	}
	return Row{Ref: job.Ref, Label: job.Purpose, State: state, JobRef: job.Ref}
}

func runRow(run inspect.Run) Row {
	return Row{Ref: "execution/" + run.ID, Label: run.JobRef, State: run.Status, JobRef: run.JobRef}
}

// Detail returns bounded human-readable log text or pretty JSON for an inspector.
func Detail(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref, mode string) (string, error) {
	if pool == nil || cfg == nil {
		return "", errors.New("tui detail requires pool and config")
	}
	var value any
	switch mode {
	case "inspect":
		var err error
		if strings.HasPrefix(ref, "health/") || (strings.HasPrefix(ref, "ddp:") && !jobs.IsSystem(ref)) {
			items, e := health.Latest(ctx, pool)
			if e != nil {
				return "", e
			}
			for _, item := range items {
				if item.Ref == ref {
					value = item
					break
				}
			}
			if value == nil {
				return "", fmt.Errorf("health observation %q not found", ref)
			}
			err = nil
		} else {
			value, err = inspect.Inspect(ctx, pool, cfg, ref)
		}
		if err != nil && strings.HasPrefix(ref, "message/") {
			value, err = comms.Show(ctx, pool, ref)
		}
		if err != nil {
			return "", err
		}
	case "diagnose":
		v, err := inspect.Diagnose(ctx, pool, cfg, ref)
		if err != nil {
			return "", err
		}
		value = v
	case "logs":
		items, err := inspect.Logs(ctx, pool, ref, projectionLimit)
		if err != nil {
			return "", err
		}
		return renderLogs(items), nil
	default:
		return "", fmt.Errorf("unknown detail mode %q", mode)
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func renderLogs(items []inspect.AttemptLog) string {
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(fmt.Sprintf("attempt %d  %s  %s", item.Number, item.Status, utc(item.StartedAt)))
		if item.PayloadExpiredAt != nil {
			b.WriteString("  [expired]")
		}
		if item.Stdout != "" {
			b.WriteString("\nstdout:\n")
			b.WriteString(item.Stdout)
		}
		if item.Stderr != "" {
			b.WriteString("\nstderr:\n")
			b.WriteString(item.Stderr)
		}
		if item.Error != nil && *item.Error != "" {
			b.WriteString("\nerror: ")
			b.WriteString(*item.Error)
		}
	}
	return b.String()
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }
