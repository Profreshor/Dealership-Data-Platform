package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/schedule"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

type PauseResult struct {
	JobRef  string `json:"job_ref"`
	Paused  bool   `json:"paused"`
	Changed bool   `json:"changed"`
}

// SetPaused records elapsed ticks before resuming, including when the scheduler
// was offline, so a restart cannot catch up work from the paused interval.
func SetPaused(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string, paused bool) (PauseResult, error) {
	result := PauseResult{JobRef: ref, Paused: paused}
	action := "jobs.resume"
	if paused {
		action = "jobs.pause"
	}
	if _, err := SelectedJobs(cfg, ref, false); err != nil {
		return result, err
	}
	if pool == nil {
		return result, errors.New("job control requires a database")
	}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.job_overrides(job_ref,paused) VALUES($1,false) ON CONFLICT DO NOTHING`, ref); err != nil {
			return err
		}
		var previous bool
		if err := tx.QueryRow(ctx, `SELECT paused FROM ops.job_overrides WHERE job_ref=$1 FOR UPDATE`, ref).Scan(&previous); err != nil {
			return err
		}
		result.Changed = previous != paused
		if !result.Changed {
			return audit.Record(ctx, tx, action, ref, result)
		}
		if paused {
			if _, err := tx.Exec(ctx, `UPDATE ops.job_overrides SET paused=true,updated_at=clock_timestamp() WHERE job_ref=$1`, ref); err != nil {
				return err
			}
		}
		job := jobs.Definitions(cfg)[ref]
		if job.Schedule != "" {
			if err := planJob(ctx, tx, cfg, ref, job, time.Now().UTC().Truncate(time.Minute)); err != nil {
				return err
			}
		}
		// Queued attempts and waiting descendants are future work. Already-running
		// attempts keep their context and may finish normally.
		if _, err := tx.Exec(ctx, `UPDATE ops.executions SET status='skipped',finished_at=clock_timestamp(),reason='job paused' WHERE job_ref=$1 AND status IN ('queued','waiting')`, ref); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE ops.job_overrides SET paused=$2,updated_at=clock_timestamp() WHERE job_ref=$1`, ref, paused); err != nil {
			return err
		}
		return audit.Record(ctx, tx, action, ref, result)
	})
	return result, dbError(err)
}

type QueuedChain struct {
	ExecutionID string   `json:"execution_id"`
	Jobs        []string `json:"jobs"`
	Status      string   `json:"status"`
}

// QueueRun is explicit asynchronous dispatch for --with-downstream.
func QueueRun(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string, downstream bool, confirmed []string) (QueuedChain, error) {
	refs, err := SelectedJobs(cfg, ref, downstream)
	if err != nil {
		return QueuedChain{}, err
	}
	if err := ConfirmStrategies(cfg, refs, confirmed); err != nil {
		return QueuedChain{}, err
	}
	if pool == nil {
		return QueuedChain{}, errors.New("job control requires a database")
	}
	result := QueuedChain{Jobs: refs, Status: "queued"}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := refusePaused(ctx, tx, refs); err != nil {
			return err
		}
		result.ExecutionID, err = enqueueRefs(ctx, tx, cfg, refs, time.Now().UTC())
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, "jobs.run", ref, result)
	})
	return result, dbError(err)
}

type BackfillResult struct {
	BackfillPreview
	Queued int64 `json:"queued"`
}

func Backfill(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string, from, through time.Time, downstream bool, confirmed []string) (BackfillResult, error) {
	preview, err := BackfillPlan(cfg, ref, from, through, downstream)
	if err != nil {
		return BackfillResult{}, err
	}
	result := BackfillResult{BackfillPreview: preview}
	if pool == nil {
		return result, errors.New("job control requires a database")
	}
	if err := ConfirmStrategies(cfg, preview.Jobs, confirmed); err != nil {
		return result, err
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := refusePaused(ctx, tx, preview.Jobs); err != nil {
			return err
		}
		job := jobs.Definitions(cfg)[ref]
		err := schedule.Each(job.Schedule, cfg.Ddp.Timezone, from.Add(-time.Nanosecond), through, func(point schedule.Occurrence) error {
			if _, err := enqueueRefs(ctx, tx, cfg, preview.Jobs, point.At); err != nil {
				return err
			}
			result.Queued += int64(len(preview.Jobs))
			return nil
		})
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, "jobs.backfill", ref, result)
	})
	if err != nil {
		result.Queued = 0
	}
	return result, dbError(err)
}

func refusePaused(ctx context.Context, tx pgx.Tx, refs []string) error {
	ordered := append([]string{}, refs...)
	slices.Sort(ordered)
	if _, err := tx.Exec(ctx, `INSERT INTO ops.job_overrides(job_ref,paused) SELECT ref,false FROM unnest($1::text[]) refs(ref) ORDER BY ref ON CONFLICT DO NOTHING`, ordered); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT job_ref,paused FROM ops.job_overrides WHERE job_ref=ANY($1) ORDER BY job_ref FOR SHARE`, ordered)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		var paused bool
		if err := rows.Scan(&ref, &paused); err != nil {
			return err
		}
		if paused {
			return fmt.Errorf("%w: %s is paused", audit.ErrRefused, ref)
		}
	}
	return rows.Err()
}

type Cancellation struct {
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	Requested   bool   `json:"requested"`
}

// Cancel leaves a running execution running until its worker confirms cleanup.
// Queued work has no process and can be interrupted in this transaction.
func Cancel(ctx context.Context, pool *pgxpool.Pool, ref string) (Cancellation, error) {
	id, ok := strings.CutPrefix(ref, "execution/")
	if _, err := ulid.ParseStrict(id); !ok || err != nil {
		return Cancellation{}, errors.New("expected execution/<ULID>")
	}
	result := Cancellation{ExecutionID: id}
	if pool == nil {
		return result, errors.New("job control requires a database")
	}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var requested bool
		if err := tx.QueryRow(ctx, `SELECT status,cancel_requested_at IS NOT NULL FROM ops.executions WHERE id=$1 FOR UPDATE`, id).Scan(&result.Status, &requested); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("execution not found")
			}
			return err
		}
		if requested {
			result.Requested = true
			return audit.Record(ctx, tx, "runs.cancel", ref, result)
		}
		switch result.Status {
		case "queued", "waiting":
			result.Status = "interrupted"
		case "running":
		default:
			return audit.Record(ctx, tx, "runs.cancel", ref, result)
		}
		result.Requested = true
		if _, err := tx.Exec(ctx, `UPDATE ops.executions SET cancel_requested_at=clock_timestamp(),reason='operator interrupted',status=$2,finished_at=CASE WHEN $2='interrupted' THEN clock_timestamp() ELSE finished_at END WHERE id=$1`, id, result.Status); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "runs.cancel", ref, result)
	})
	return result, dbError(err)
}
