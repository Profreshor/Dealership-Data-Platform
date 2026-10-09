package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Outbox reports aggregate delivery pressure without reading message content.
// ponytail: scans retained outbox metadata; add indexed active-state summaries if history makes this slow.
func Outbox(ctx context.Context, pool *pgxpool.Pool) Result {
	result := Result{State: "unknown", Severity: "warning", Message: "Cannot read outbox state."}
	if pool == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return result
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '3s'"); err != nil {
		return result
	}
	var failed, active int64
	var oldest, dbNow *time.Time
	var future *bool
	err = tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'failed' AND content_expired_at IS NULL),
		       count(*) FILTER (WHERE status IN ('pending', 'delivering')),
		       min(created_at) FILTER (WHERE status IN ('pending', 'delivering')),
		       bool_or(created_at > clock_timestamp()), clock_timestamp()
		FROM ops.outbox`).Scan(&failed, &active, &oldest, &future, &dbNow)
	if err != nil || dbNow == nil || (future != nil && *future) {
		return result
	}
	value := fmt.Sprintf("active=%d failed=%d", active, failed)
	if oldest != nil {
		age := dbNow.Sub(*oldest)
		if age < 0 {
			return result
		}
		value += fmt.Sprintf(" oldest=%s", age.Round(time.Second))
		if age > 15*time.Minute {
			result.State = "failing"
			result.Message = "Outbox has messages waiting for delivery."
			result.Value = value
			return result
		}
	}
	result.Value = value
	if failed > 0 || active > 1000 {
		result.State = "failing"
		result.Message = "Outbox has messages needing delivery attention."
		return result
	}
	result.State = "ok"
	result.Message = "Outbox delivery queue is healthy."
	return result
}
