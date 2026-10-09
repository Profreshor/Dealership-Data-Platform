// Package inspect provides read-only operational history queries.
package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

type Run struct {
	ID                string     `json:"id"`
	JobRef            string     `json:"job_ref"`
	Dispatch          string     `json:"dispatch"`
	ChainID           *string    `json:"chain_id,omitempty"`
	DependsOn         []string   `json:"depends_on,omitempty"`
	Reason            *string    `json:"reason,omitempty"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	ScheduledAt       time.Time  `json:"scheduled_at"`
	AvailableAt       time.Time  `json:"available_at"`
	MaxAttempts       int        `json:"max_attempts"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	Status            string     `json:"status"`
}

type RunDetail struct {
	Run
	Attempts      []AttemptLog `json:"attempts"`
	TotalAttempts int          `json:"total_attempts"`
}

type AttemptLog struct {
	ID               string          `json:"id"`
	ExecutionID      string          `json:"execution_id"`
	Number           int             `json:"number"`
	StartedAt        time.Time       `json:"started_at"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
	Status           string          `json:"status"`
	Stdout           string          `json:"stdout"`
	Stderr           string          `json:"stderr"`
	Result           json.RawMessage `json:"result,omitempty"`
	Error            *string         `json:"error,omitempty"`
	PayloadExpiredAt *time.Time      `json:"payload_expired_at,omitempty"`
}

const maxLimit = 100

func ListRuns(ctx context.Context, pool *pgxpool.Pool, jobRef string, limit int) ([]Run, error) {
	if err := validateLimit(limit); err != nil {
		return []Run{}, err
	}
	if jobRef != "" {
		if err := validateInspectionRef(jobRef); err != nil {
			return []Run{}, err
		}
	}
	query := `SELECT id, job_ref, dispatch, chain_id, depends_on, reason, cancel_requested_at, scheduled_at, available_at, max_attempts, started_at, finished_at, status
FROM ops.executions
WHERE ($1 = '' OR job_ref = $1)
ORDER BY scheduled_at DESC, id DESC
LIMIT $2`
	rows, err := pool.Query(ctx, query, jobRef, limit)
	if err != nil {
		return []Run{}, queryError(err)
	}
	defer rows.Close()
	result := make([]Run, 0, limit)
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

func ShowRun(ctx context.Context, pool *pgxpool.Pool, ref string) (RunDetail, error) {
	if err := validateExecutionRef(ref); err != nil {
		return RunDetail{Attempts: []AttemptLog{}}, err
	}
	var detail RunDetail
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return RunDetail{Attempts: []AttemptLog{}}, queryError(err)
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT id, job_ref, dispatch, chain_id, depends_on, reason, cancel_requested_at, scheduled_at, available_at, max_attempts, started_at, finished_at, status
FROM ops.executions WHERE id = $1`, strings.TrimPrefix(ref, "execution/")).Scan(
		&detail.ID, &detail.JobRef, &detail.Dispatch, &detail.ChainID, &detail.DependsOn, &detail.Reason, &detail.CancelRequestedAt, &detail.ScheduledAt, &detail.AvailableAt, &detail.MaxAttempts, &detail.StartedAt, &detail.FinishedAt, &detail.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RunDetail{Attempts: []AttemptLog{}}, fmt.Errorf("run %q not found", ref)
		}
		return RunDetail{Attempts: []AttemptLog{}}, queryError(err)
	}
	detail.Attempts, err = attempts(ctx, tx, detail.ID, maxLimit)
	if err != nil {
		return RunDetail{Attempts: []AttemptLog{}}, err
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM ops.attempts WHERE execution_id = $1", detail.ID).Scan(&detail.TotalAttempts); err != nil {
		return RunDetail{Attempts: []AttemptLog{}}, queryError(err)
	}
	return detail, nil
}

func Logs(ctx context.Context, pool *pgxpool.Pool, ref string, limit int) ([]AttemptLog, error) {
	if err := validateLimit(limit); err != nil {
		return []AttemptLog{}, err
	}
	var executionID, jobRef string
	switch {
	case strings.HasPrefix(ref, "execution/"):
		if err := validateExecutionRef(ref); err != nil {
			return []AttemptLog{}, err
		}
		executionID = strings.TrimPrefix(ref, "execution/")
	case strings.HasPrefix(ref, "job/"):
		if err := validateInspectionRef(ref); err != nil {
			return []AttemptLog{}, err
		}
		jobRef = ref
	case strings.HasPrefix(ref, "model/") || jobs.IsSystem(ref):
		if err := validateInspectionRef(ref); err != nil {
			return []AttemptLog{}, err
		}
		jobRef = ref
	default:
		return []AttemptLog{}, fmt.Errorf("invalid operational reference %q", ref)
	}
	if executionID != "" {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ops.executions WHERE id = $1)", executionID).Scan(&exists); err != nil {
			return []AttemptLog{}, queryError(err)
		}
		if !exists {
			return []AttemptLog{}, fmt.Errorf("run %q not found", ref)
		}
	}
	return logs(ctx, pool, executionID, jobRef, limit)
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func attempts(ctx context.Context, q queryer, executionID string, limit int) ([]AttemptLog, error) {
	return logs(ctx, q, executionID, "", limit)
}

func logs(ctx context.Context, q queryer, executionID, jobRef string, limit int) ([]AttemptLog, error) {
	rows, err := q.Query(ctx, `SELECT a.id, a.execution_id, a.number, a.started_at, a.finished_at, a.status, CASE WHEN a.payload_expired_at IS NULL THEN a.stdout ELSE '' END, CASE WHEN a.payload_expired_at IS NULL THEN a.stderr ELSE '' END, CASE WHEN a.payload_expired_at IS NULL THEN a.result END, CASE WHEN a.payload_expired_at IS NULL THEN a.error END, a.payload_expired_at
FROM ops.attempts a JOIN ops.executions e ON e.id = a.execution_id
WHERE ($1 = '' OR a.execution_id = $1) AND ($2 = '' OR e.job_ref = $2)
ORDER BY a.started_at DESC, a.id DESC LIMIT $3`, executionID, jobRef, limit)
	if err != nil {
		return []AttemptLog{}, queryError(err)
	}
	defer rows.Close()
	result := make([]AttemptLog, 0, limit)
	for rows.Next() {
		var log AttemptLog
		if err := rows.Scan(&log.ID, &log.ExecutionID, &log.Number, &log.StartedAt, &log.FinishedAt, &log.Status, &log.Stdout, &log.Stderr, &log.Result, &log.Error, &log.PayloadExpiredAt); err != nil {
			return []AttemptLog{}, queryError(err)
		}
		result = append(result, log)
	}
	if err := rows.Err(); err != nil {
		return []AttemptLog{}, queryError(err)
	}
	return result, nil
}

func validateLimit(limit int) error {
	if limit < 1 || limit > maxLimit {
		return fmt.Errorf("limit must be between 1 and %d", maxLimit)
	}
	return nil
}

func validateJobRef(ref string) error {
	name, ok := strings.CutPrefix(ref, "job/")
	if !ok || name == "" || strings.Contains(name, "/") || strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return fmt.Errorf("invalid job reference %q", ref)
	}
	return nil
}

func validateExecutionRef(ref string) error {
	id, ok := strings.CutPrefix(ref, "execution/")
	if !ok || id == "" {
		return fmt.Errorf("invalid execution reference %q", ref)
	}
	if _, err := ulid.ParseStrict(id); err != nil {
		return fmt.Errorf("invalid execution reference %q", ref)
	}
	return nil
}

func queryError(error) error { return errors.New("operational history query failed") }
