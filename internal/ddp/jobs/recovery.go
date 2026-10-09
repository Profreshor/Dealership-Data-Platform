package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

const (
	recoveryAttemptError = "scheduler shutdown interrupted active attempt"
	recoveryEventMessage = "scheduler recovered execution after shutdown"
)

// RecoverScheduler reconciles scheduler work left behind by a crashed or
// stopped scheduler. The caller must hold the scheduler advisory lock.
func RecoverScheduler(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	if pool == nil {
		return 0, errors.New("scheduler recovery requires a pool")
	}
	count := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT e.id, e.max_attempts, e.retry_initial_ns, e.retry_max_ns, e.cancel_requested_at IS NOT NULL,
			       COALESCE((SELECT max(a.number) FROM ops.attempts a WHERE a.execution_id=e.id), 0)
			FROM ops.executions e
			WHERE e.dispatch='scheduler' AND
			      (e.status='running' OR (e.status='interrupted' AND e.reason='scheduler shutdown'))
			ORDER BY e.id
			FOR UPDATE OF e`)
		if err != nil {
			return err
		}
		defer rows.Close()
		type execution struct {
			id                         string
			maxAttempts, actualAttempt int
			initial, maximum           time.Duration
			cancelled                  bool
		}
		var executions []execution
		for rows.Next() {
			var e execution
			if err := rows.Scan(&e.id, &e.maxAttempts, &e.initial, &e.maximum, &e.cancelled, &e.actualAttempt); err != nil {
				return err
			}
			executions = append(executions, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range executions {
			attemptError := recoveryAttemptError
			if e.cancelled {
				attemptError = ErrOperatorCancelled.Error()
			}
			if _, err := tx.Exec(ctx, `UPDATE ops.attempts SET status='interrupted', finished_at=now(), error=$2 WHERE execution_id=$1 AND status='running'`, e.id, attemptError); err != nil {
				return err
			}
			status := "failed"
			available := time.Time{}
			if e.cancelled {
				status = "interrupted"
			} else if e.actualAttempt < e.maxAttempts {
				status = "queued"
				available = time.Now().UTC().Add(retryDelay(e.actualAttempt, e.initial, e.maximum))
			}
			if _, err := tx.Exec(ctx, `UPDATE ops.executions SET status=$2, finished_at=CASE WHEN $2='queued' THEN NULL ELSE now() END, available_at=CASE WHEN $2='queued' THEN $3 ELSE available_at END, reason=CASE WHEN $2='queued' THEN NULL WHEN $2='interrupted' THEN 'operator interrupted' ELSE 'scheduler shutdown' END WHERE id=$1`, e.id, status, available); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO ops.events(id,resource_ref,kind,message) VALUES($1,$2,'recovered',$3)`, ulid.Make().String(), "execution/"+e.id, recoveryEventMessage); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		count = 0
	}
	return count, executionError("recover scheduler executions", err)
}
