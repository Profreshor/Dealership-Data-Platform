package inspect

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job is the operator view of a declared or historically observed execution root.
type Job struct {
	Ref            string   `json:"ref"`
	Purpose        string   `json:"purpose,omitempty"`
	Action         string   `json:"action,omitempty"`
	Schedule       string   `json:"schedule,omitempty"`
	After          []string `json:"after,omitempty"`
	ConcurrencyKey string   `json:"concurrency_key,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Paused         bool     `json:"paused"`
	NewestRun      *Run     `json:"newest_run,omitempty"`
}

// ListJobs combines current declarations with persisted history in one readonly snapshot.
// History is retained for refs removed from the registry so operators can still investigate them.
func ListJobs(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) ([]Job, error) {
	if pool == nil || cfg == nil {
		return []Job{}, fmt.Errorf("job inspection requires pool and config")
	}
	defs := definitions(cfg)
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return []Job{}, queryError(err)
	}
	defer tx.Rollback(ctx)
	overrides, err := tx.Query(ctx, "SELECT job_ref, paused FROM ops.job_overrides")
	if err != nil {
		return []Job{}, queryError(err)
	}
	for overrides.Next() {
		var ref string
		var paused bool
		if err := overrides.Scan(&ref, &paused); err != nil {
			overrides.Close()
			return []Job{}, queryError(err)
		}
		job := defs[ref]
		job.Ref, job.Paused = ref, paused
		defs[ref] = job
	}
	if err := overrides.Err(); err != nil {
		overrides.Close()
		return []Job{}, queryError(err)
	}
	overrides.Close()
	rows, err := tx.Query(ctx, newestJobsQuery)
	if err != nil {
		return []Job{}, queryError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		var paused bool
		var run Run
		if err := rows.Scan(&ref, &paused, &run.ID, &run.JobRef, &run.Dispatch, &run.ChainID, &run.DependsOn, &run.Reason, &run.CancelRequestedAt, &run.ScheduledAt, &run.AvailableAt, &run.MaxAttempts, &run.StartedAt, &run.FinishedAt, &run.Status); err != nil {
			return []Job{}, queryError(err)
		}
		job := defs[ref]
		job.Ref, job.Paused, job.NewestRun = ref, paused, &run
		defs[ref] = job
	}
	if err := rows.Err(); err != nil {
		return []Job{}, queryError(err)
	}
	result := make([]Job, 0, len(defs))
	for _, job := range defs {
		result = append(result, job)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Ref < result[j].Ref })
	return result, nil
}

func ShowJob(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string) (Job, error) {
	if pool == nil || cfg == nil {
		return Job{}, fmt.Errorf("job inspection requires pool and config")
	}
	if err := validateInspectionRef(ref); err != nil {
		return Job{}, err
	}
	job, declared := definitions(cfg)[ref]
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Job{}, queryError(err)
	}
	defer tx.Rollback(ctx)
	var paused bool
	if err := tx.QueryRow(ctx, "SELECT COALESCE((SELECT paused FROM ops.job_overrides WHERE job_ref=$1),false)", ref).Scan(&paused); err != nil {
		return Job{}, queryError(err)
	}
	var run Run
	err = tx.QueryRow(ctx, newestJobQuery, ref).Scan(&run.ID, &run.JobRef, &run.Dispatch, &run.ChainID, &run.DependsOn, &run.Reason, &run.CancelRequestedAt, &run.ScheduledAt, &run.AvailableAt, &run.MaxAttempts, &run.StartedAt, &run.FinishedAt, &run.Status)
	if err != nil && err != pgx.ErrNoRows {
		return Job{}, queryError(err)
	}
	if err == pgx.ErrNoRows && !declared {
		return Job{}, fmt.Errorf("job %q not found", ref)
	}
	job.Ref, job.Paused = ref, paused
	if err == nil {
		job.NewestRun = &run
	}
	return job, nil
}

const newestJobsQuery = `SELECT e.job_ref, COALESCE(o.paused,false), e.id, e.job_ref, e.dispatch, e.chain_id, e.depends_on, e.reason, e.cancel_requested_at, e.scheduled_at, e.available_at, e.max_attempts, e.started_at, e.finished_at, e.status
FROM (SELECT DISTINCT ON (job_ref) * FROM ops.executions ORDER BY job_ref, scheduled_at DESC, id DESC) e
LEFT JOIN ops.job_overrides o ON o.job_ref=e.job_ref`
const newestJobQuery = `SELECT id, job_ref, dispatch, chain_id, depends_on, reason, cancel_requested_at, scheduled_at, available_at, max_attempts, started_at, finished_at, status
FROM ops.executions WHERE job_ref=$1 ORDER BY scheduled_at DESC, id DESC LIMIT 1`

func definitions(cfg *config.Config) map[string]Job {
	declared := jobs.Definitions(cfg)
	result := make(map[string]Job, len(declared))
	for ref, def := range declared {
		result[ref] = Job{Ref: ref, Purpose: def.Purpose, Action: def.Action, Schedule: def.Schedule, After: append([]string(nil), def.After...), ConcurrencyKey: def.ConcurrencyKey, Tags: append([]string(nil), def.Tags...)}
	}
	return result
}

func validateInspectionRef(ref string) error {
	if strings.HasPrefix(ref, "job/") {
		return validateJobRef(ref)
	}
	if strings.HasPrefix(ref, "model/") {
		name := strings.TrimPrefix(ref, "model/")
		if name == "" || strings.Contains(name, "/") || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return fmt.Errorf("invalid model reference %q", ref)
		}
		return nil
	}
	if jobs.IsSystem(ref) {
		return nil
	}
	return fmt.Errorf("invalid operational reference %q", ref)
}
