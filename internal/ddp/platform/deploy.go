package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deployment reports the latest host-written release outcome. It never writes
// deployment state and keeps database failures opaque to operators.
func Deployment(ctx context.Context, pool *pgxpool.Pool) *Result {
	unknown := &Result{State: "unknown", Severity: "critical", Message: "Deployment evidence is unavailable."}
	if pool == nil {
		return unknown
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var status, phase string
	var finished time.Time
	err := pool.QueryRow(ctx, `SELECT status,phase,finished_at FROM ops.deployments ORDER BY finished_at DESC,id DESC LIMIT 1`).Scan(&status, &phase, &finished)
	if err != nil && err != pgx.ErrNoRows {
		return unknown
	}
	if err == pgx.ErrNoRows {
		return nil
	}
	if status != "succeeded" && status != "failed" {
		return unknown
	}
	result := &Result{State: "ok", Severity: "critical", Value: fmt.Sprintf("phase=%s finished_at=%s", phase, finished.UTC().Format(time.RFC3339Nano))}
	if status == "failed" {
		result.State = "failing"
		result.Message = fmt.Sprintf("The latest deployment failed during the %s phase.", phase)
	} else {
		result.Message = "The latest deployment completed successfully."
	}
	return result
}
