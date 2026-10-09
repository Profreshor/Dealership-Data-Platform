package health

import (
	"context"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Platform reads the same checks available to local operators without recording alerts.
func Platform(ctx context.Context, pool *pgxpool.Pool, root string, cfg *config.Config) []Observation {
	checks := []struct {
		ref, target string
		result      platform.Result
	}{
		{"ddp:database", "service/postgres", platform.Database(ctx, pool)},
		{"ddp:scheduler", "service/scheduler", platform.Scheduler(ctx, pool)},
		{"ddp:disk", "", platform.Disk(root)},
		{"ddp:outbox", "", platform.Outbox(ctx, pool)},
	}
	observations := make([]Observation, 0, len(checks))
	for _, check := range checks {
		observations = append(observations, Observation{Ref: check.ref, Target: check.target, State: check.result.State, Severity: check.result.Severity, Message: check.result.Message, Value: check.result.Value, Notify: []string{"group/platform_ops"}})
	}
	if deployment := platform.Deployment(ctx, pool); deployment != nil {
		observations = append(observations, Observation{Ref: "ddp:deployment", State: deployment.State, Severity: deployment.Severity, Message: deployment.Message, Value: deployment.Value, Notify: []string{"group/platform_ops"}})
	}
	if cfg != nil && cfg.Deploy.Backup != nil {
		check := platform.Backup(ctx, pool, *cfg.Deploy.Backup)
		observations = append(observations, Observation{Ref: "ddp:backup", State: check.State, Severity: check.Severity, Message: check.Message, Value: check.Value, Notify: []string{"group/platform_ops"}})
	}
	return observations
}
