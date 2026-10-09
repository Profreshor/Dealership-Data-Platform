package cleanup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxRun = 40 * time.Second
)

// Run expires operational evidence in bounded, independently committed batches.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) (Counts, error) {
	var counts Counts
	if cfg == nil {
		return counts, errors.New("cleanup requires config")
	}
	policy := cfg.RetentionPolicy()
	runLogs, err := positiveDuration(policy.RunLogs)
	if err != nil {
		return counts, fmt.Errorf("invalid run log retention: %w", err)
	}
	health, err := positiveDuration(policy.Health)
	if err != nil {
		return counts, fmt.Errorf("invalid health retention: %w", err)
	}
	messages, err := positiveDuration(policy.Messages)
	if err != nil {
		return counts, fmt.Errorf("invalid message retention: %w", err)
	}
	if pool == nil {
		return counts, errors.New("cleanup requires database")
	}

	workCtx, cancel := context.WithTimeout(ctx, maxRun)
	defer cancel()
	clockCtx, cancelClock := context.WithTimeout(workCtx, 3*time.Second)
	var now time.Time
	err = pool.QueryRow(clockCtx, `SELECT clock_timestamp()`).Scan(&now)
	cancelClock()
	if err != nil {
		if ctx.Err() != nil {
			return counts, ctx.Err()
		}
		return counts, cleanupError(err)
	}
	cutoffs := [3]time.Time{now.Add(-runLogs), now.Add(-health), now.Add(-messages)}
	for {
		if err := ctx.Err(); err != nil {
			return counts, err
		}
		if err := workCtx.Err(); err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				counts.More = true
				return counts, nil
			}
			return counts, err
		}
		var batch Counts
		err := pgx.BeginFunc(workCtx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(workCtx, `SET LOCAL statement_timeout = '3s'`); err != nil {
				return err
			}
			var err error
			batch.RunLogs, err = affected(workCtx, tx, `
WITH picked AS (
  SELECT a.id FROM ops.attempts a JOIN ops.executions e ON e.id=a.execution_id
  WHERE a.payload_expired_at IS NULL AND a.finished_at < $1
    AND a.status IN ('succeeded','failed','interrupted')
    AND e.status IN ('succeeded','failed','interrupted','skipped')
  ORDER BY a.finished_at,a.id LIMIT 1000 FOR UPDATE OF a SKIP LOCKED
), changed AS (
  UPDATE ops.attempts a SET stdout='',stderr='',result=NULL,error=NULL,payload_expired_at=$2
  FROM picked WHERE a.id=picked.id RETURNING 1
) SELECT count(*) FROM changed`, cutoffs[0], now)
			if err != nil {
				return err
			}
			batch.Messages, err = affected(workCtx, tx, `
WITH picked AS (
  SELECT id FROM ops.outbox
  WHERE content_expired_at IS NULL AND finished_at < $1 AND status IN ('delivered','failed')
  ORDER BY finished_at,id LIMIT 1000 FOR UPDATE SKIP LOCKED
), changed AS (
  UPDATE ops.outbox o SET context='{}'::jsonb,subject=NULL,text_body=NULL,html_body=NULL,content_expired_at=$2
  FROM picked WHERE o.id=picked.id RETURNING 1
) SELECT count(*) FROM changed`, cutoffs[2], now)
			if err != nil {
				return err
			}
			batch.HealthDeleted, err = affected(workCtx, tx, `
WITH picked AS (
  SELECT e.id FROM ops.health_evaluations e
  WHERE e.observed_at < $1
    AND NOT EXISTS (SELECT 1 FROM ops.alert_state s WHERE s.evaluation_id=e.id)
    AND NOT EXISTS (SELECT 1 FROM ops.alerts a WHERE a.evaluation_id=e.id)
  ORDER BY e.observed_at,e.id LIMIT 1000 FOR UPDATE SKIP LOCKED
), removed AS (
  DELETE FROM ops.health_evaluations e USING picked WHERE e.id=picked.id RETURNING 1
) SELECT count(*) FROM removed`, cutoffs[1])
			if err != nil {
				return err
			}
			batch.HealthExpired, err = affected(workCtx, tx, `
WITH picked AS (
  SELECT e.id FROM ops.health_evaluations e
  WHERE e.evidence_expired_at IS NULL AND e.observed_at < $1
    AND EXISTS (SELECT 1 FROM ops.alerts a WHERE a.evaluation_id=e.id)
    AND NOT EXISTS (SELECT 1 FROM ops.alert_state s WHERE s.evaluation_id=e.id)
    AND NOT EXISTS (SELECT 1 FROM ops.alerts a WHERE a.evaluation_id=e.id AND a.message_id IS NULL)
  ORDER BY e.observed_at,e.id LIMIT 1000 FOR UPDATE SKIP LOCKED
), changed AS (
  UPDATE ops.health_evaluations e SET observation=jsonb_strip_nulls(jsonb_build_object(
    'ref',e.observation->'ref','target',e.observation->'target','state',e.state,
    'severity',e.severity,'execution_id',e.observation->'execution_id',
    'notify',e.observation->'notify','message','Health evidence expired.'
  )),evidence_expired_at=$2 FROM picked WHERE e.id=picked.id RETURNING 1
) SELECT count(*) FROM changed`, cutoffs[1], now)
			if err != nil {
				return err
			}
			batch.AlertContexts, err = affected(workCtx, tx, `
WITH picked AS (
  SELECT a.id FROM ops.alerts a JOIN ops.outbox o ON o.id=a.message_id
  WHERE o.content_expired_at IS NOT NULL AND a.context <> '{}'::jsonb
  ORDER BY a.created_at,a.id LIMIT 1000 FOR UPDATE OF a SKIP LOCKED
), changed AS (
  UPDATE ops.alerts a SET context='{}'::jsonb FROM picked WHERE a.id=picked.id RETURNING 1
) SELECT count(*) FROM changed`)
			if err != nil {
				return err
			}
			batch.ModelErrors, err = affected(workCtx, tx, `
WITH picked AS (
  SELECT id FROM ops.model_refreshes
  WHERE error IS NOT NULL AND error <> '' AND finished_at IS NOT NULL AND finished_at < $1
    AND status IN ('succeeded','failed')
  ORDER BY finished_at,id LIMIT 1000 FOR UPDATE SKIP LOCKED
), changed AS (
  UPDATE ops.model_refreshes m SET error=NULL FROM picked WHERE m.id=picked.id RETURNING 1
) SELECT count(*) FROM changed`, cutoffs[0])
			if err != nil {
				return err
			}
			batch.Sessions, err = sessionCount(workCtx, tx)
			if err != nil {
				return err
			}
			batch.PasswordTokens, err = affected(workCtx, tx, `SELECT ddp.expire_password_tokens()`)
			if err != nil {
				return err
			}
			return audit.Record(workCtx, tx, "cleanup.run", "retention", batch)
		})
		if err != nil {
			if ctx.Err() != nil {
				return counts, ctx.Err()
			}
			if errors.Is(workCtx.Err(), context.DeadlineExceeded) {
				counts.More = true
				return counts, nil
			}
			return counts, cleanupError(err)
		}
		counts.RunLogs += batch.RunLogs
		counts.Messages += batch.Messages
		counts.HealthDeleted += batch.HealthDeleted
		counts.HealthExpired += batch.HealthExpired
		counts.AlertContexts += batch.AlertContexts
		counts.ModelErrors += batch.ModelErrors
		counts.Sessions += batch.Sessions
		counts.PasswordTokens += batch.PasswordTokens
		if batch.RunLogs+batch.Messages+batch.HealthDeleted+batch.HealthExpired+batch.AlertContexts+batch.ModelErrors+batch.Sessions+batch.PasswordTokens == 0 {
			return counts, nil
		}
	}
}

func positiveDuration(value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, errors.New("must be a positive duration")
	}
	return d, nil
}

func affected(ctx context.Context, tx pgx.Tx, query string, args ...any) (int64, error) {
	var n int64
	if err := tx.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func sessionCount(ctx context.Context, tx pgx.Tx) (int64, error) {
	var n int64
	if err := tx.QueryRow(ctx, `SELECT ddp.expire_sessions()`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func cleanupError(err error) error {
	if errors.Is(err, audit.ErrRefused) {
		return audit.ErrRefused
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42501" {
		return audit.ErrRefused
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("cleanup batch failed")
}
