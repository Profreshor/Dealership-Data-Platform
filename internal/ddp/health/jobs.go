package health

import (
	"context"
	"slices"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A retry in progress is not a final failure. Only a completed success clears one.
func jobChecks(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) []Observation {
	refs := make([]string, 0, len(cfg.Jobs)+len(cfg.Models)+2)
	refs = append(refs, "ddp:cleanup")
	for name := range cfg.Jobs {
		refs = append(refs, "job/"+name)
	}
	for name, model := range cfg.Models {
		if model.Schedule != "" {
			refs = append(refs, "model/"+name)
		}
	}
	if cfg.Comms.SMTP != nil {
		refs = append(refs, "ddp:comms_relay")
	}
	if cfg.Deploy.Backup != nil {
		refs = append(refs, "ddp:backup")
	}
	slices.Sort(refs)
	result := make([]Observation, 0, len(refs))
	for _, ref := range refs {
		o := Observation{Ref: "ddp:job/" + ref, Target: ref, State: "ok", Severity: "critical", Message: "No final job failure.", Notify: []string{"group/platform_ops"}}
		var status string
		err := pool.QueryRow(ctx, `SELECT id,status FROM ops.executions WHERE job_ref=$1 AND status IN ('succeeded','failed') ORDER BY scheduled_at DESC,id DESC LIMIT 1`, ref).Scan(&o.ExecutionID, &status)
		switch {
		case err == pgx.ErrNoRows:
		case err != nil:
			o.State = "unknown"
			o.Message = "Cannot read final job status."
		case status == "failed":
			o.State = "failing"
			o.Message = "The job exhausted its execution attempts."
		default:
			o.Message = "The latest completed job succeeded."
		}
		result = append(result, o)
	}
	return result
}
