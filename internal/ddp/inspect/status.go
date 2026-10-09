package inspect

import (
	"context"
	"errors"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/scheduler"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SystemStatus struct {
	ObservedAt        time.Time           `json:"observed_at"`
	Scheduler         scheduler.State     `json:"scheduler"`
	Jobs              []Job               `json:"jobs"`
	Health            []health.Evaluation `json:"health"`
	HealthObservation string              `json:"health_observation"`
	Outbox            map[string]int64    `json:"outbox"`
	PendingAlerts     int64               `json:"pending_alerts"`
	RecentFailures    []Run               `json:"recent_failures"`
}

func Overview(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) (SystemStatus, error) {
	if pool == nil || cfg == nil {
		return SystemStatus{}, errors.New("status inspection requires pool and config")
	}
	var observedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observedAt); err != nil {
		return SystemStatus{}, queryError(err)
	}
	status := SystemStatus{
		ObservedAt:     observedAt,
		Outbox:         map[string]int64{"pending": 0, "delivering": 0, "delivered": 0, "failed": 0},
		RecentFailures: []Run{},
		Jobs:           []Job{},
		Health:         []health.Evaluation{},
	}
	var err error
	if status.Scheduler, err = scheduler.Status(ctx, pool); err != nil {
		return SystemStatus{}, err
	}
	if status.Jobs, err = ListJobs(ctx, pool, cfg); err != nil {
		return SystemStatus{}, err
	}
	if status.Health, err = health.Latest(ctx, pool); err != nil {
		return SystemStatus{}, queryError(err)
	}
	var healthFinished *time.Time
	if err := pool.QueryRow(ctx, `SELECT finished_at FROM ops.executions WHERE job_ref=$1 AND status='succeeded' AND finished_at IS NOT NULL ORDER BY finished_at DESC,id DESC LIMIT 1`, jobs.HealthRef).Scan(&healthFinished); err != nil && err != pgx.ErrNoRows {
		return SystemStatus{}, queryError(err)
	}
	expectedHealth := make([]string, 0, len(cfg.Health))
	for ref := range cfg.Health {
		expectedHealth = append(expectedHealth, "health/"+ref)
	}
	status.HealthObservation = healthObservation(status.ObservedAt, healthFinished, status.Health, expectedHealth)
	if err := loadOutbox(ctx, pool, &status); err != nil {
		return SystemStatus{}, err
	}
	if status.RecentFailures, err = failedRuns(ctx, pool); err != nil {
		return SystemStatus{}, err
	}
	return status, nil
}

func healthObservation(now time.Time, finished *time.Time, evaluations []health.Evaluation, expected []string) string {
	if finished == nil {
		return "not_observed"
	}
	if finished.After(now) || now.Sub(*finished) > 3*time.Minute {
		return "stale"
	}
	seen := make(map[string]bool, len(evaluations))
	for _, evaluation := range evaluations {
		seen[evaluation.Ref] = true
		if evaluation.ObservedAt.After(now) || now.Sub(evaluation.ObservedAt) > 3*time.Minute {
			return "stale"
		}
	}
	for _, ref := range expected {
		if !seen[ref] {
			return "stale"
		}
	}
	return "current"
}

func loadOutbox(ctx context.Context, pool *pgxpool.Pool, status *SystemStatus) error {
	rows, err := pool.Query(ctx, `SELECT status,count(*) FROM ops.outbox GROUP BY status`)
	if err != nil {
		return queryError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return queryError(err)
		}
		status.Outbox[state] = count
	}
	if err := rows.Err(); err != nil {
		return queryError(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.alerts WHERE message_id IS NULL`).Scan(&status.PendingAlerts); err != nil {
		return queryError(err)
	}
	return nil
}

func failedRuns(ctx context.Context, pool *pgxpool.Pool) ([]Run, error) {
	rows, err := pool.Query(ctx, `SELECT id,job_ref,dispatch,chain_id,depends_on,reason,cancel_requested_at,scheduled_at,available_at,max_attempts,started_at,finished_at,status FROM ops.executions WHERE status='failed' ORDER BY scheduled_at DESC,id DESC LIMIT 20`)
	if err != nil {
		return []Run{}, queryError(err)
	}
	defer rows.Close()
	result := make([]Run, 0, 20)
	for rows.Next() {
		var run Run
		if err := rows.Scan(&run.ID, &run.JobRef, &run.Dispatch, &run.ChainID, &run.DependsOn, &run.Reason, &run.CancelRequestedAt, &run.ScheduledAt, &run.AvailableAt, &run.MaxAttempts, &run.StartedAt, &run.FinishedAt, &run.Status); err != nil {
			return []Run{}, queryError(err)
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		return []Run{}, queryError(err)
	}
	return result, nil
}
