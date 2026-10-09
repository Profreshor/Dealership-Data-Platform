package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// Enqueue persists one logical execution before any workload starts. Retry policy
// belongs to this execution; later registry edits only affect new executions.
func Enqueue(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, ref string, scheduledAt time.Time) (Execution, error) {
	if _, _, err := jobDefinition(cfg, ref); err != nil {
		return Execution{}, err
	}
	if pool == nil {
		return Execution{}, errors.New("execution requires a pool")
	}
	var run Execution
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		run, err = EnqueueTx(ctx, tx, cfg, ref, scheduledAt)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, "jobs.run", ref, run)
	})
	return run, err
}

// EnqueueTx lets the scheduler save its tick and complete chain atomically.
func EnqueueTx(ctx context.Context, tx pgx.Tx, cfg *config.Config, ref string, scheduledAt time.Time) (Execution, error) {
	job, _, err := jobDefinition(cfg, ref)
	if err != nil {
		return Execution{}, err
	}
	if tx == nil || scheduledAt.IsZero() {
		return Execution{}, errors.New("execution requires a pool and scheduled time")
	}
	retry := cfg.Scheduler.Defaults.Retry
	if job.Retry != nil {
		retry = *job.Retry
	}
	initial, err := time.ParseDuration(retry.InitialDelay)
	if err != nil || initial <= 0 {
		return Execution{}, errors.New("invalid retry initial delay")
	}
	maximum, err := time.ParseDuration(retry.MaxDelay)
	if err != nil || maximum < initial || retry.MaxAttempts < 1 {
		return Execution{}, errors.New("invalid retry policy")
	}
	run := Execution{ID: ulid.Make().String(), Job: ref, Status: "queued"}
	_, err = tx.Exec(ctx, `INSERT INTO ops.executions(id,job_ref,scheduled_at,status,max_attempts,retry_initial_ns,retry_max_ns) VALUES($1,$2,$3,'queued',$4,$5,$6)`, run.ID, ref, scheduledAt, retry.MaxAttempts, int64(initial), int64(maximum))
	return run, executionError("enqueue execution", err)
}

// Run creates a new execution and waits for its attempts, including delayed retries.
func Run(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root, ref string) (Execution, error) {
	run, err := Enqueue(ctx, pool, cfg, ref, time.Now().UTC())
	if err != nil {
		return run, err
	}
	for {
		run, err = RunExecution(ctx, pool, cfg, root, "execution/"+run.ID)
		if ctx.Err() != nil && run.Status == "" {
			return interruptQueued(ctx, pool, run)
		}
		if run.Status != "queued" {
			return run, err
		}
		var delay time.Duration
		if e := pool.QueryRow(ctx, `SELECT GREATEST(0, EXTRACT(EPOCH FROM (available_at-clock_timestamp()))*1000000000)::bigint FROM ops.executions WHERE id=$1`, run.ID).Scan(&delay); e != nil {
			if ctx.Err() != nil {
				return interruptQueued(ctx, pool, run)
			}
			return run, executionError("read retry time", e)
		}
		timer := time.NewTimer(max(delay, 25*time.Millisecond))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return interruptQueued(ctx, pool, run)
		}
	}
}

// RunExecution atomically claims a due execution and performs one attempt. A
// failed attempt may leave the execution queued with a future available_at.
func RunExecution(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root, ref string) (Execution, error) {
	id, ok := strings.CutPrefix(ref, "execution/")
	if _, err := ulid.ParseStrict(id); !ok || err != nil {
		return Execution{}, errors.New("expected execution/<ULID>")
	}
	if pool == nil {
		return Execution{}, errors.New("execution requires a pool")
	}
	run := Execution{ID: id}
	locks, err := executionLocks(ctx, pool, cfg, id)
	if locks != nil {
		defer closeLocks(locks)
	}
	if err != nil {
		if errors.Is(err, errExecutionBusy) {
			if readErr := pool.QueryRow(ctx, `SELECT status FROM ops.executions WHERE id=$1`, id).Scan(&run.Status); readErr != nil {
				return run, executionError("read busy execution", readErr)
			}
		}
		if errors.Is(err, errJobBusy) {
			// Avoid immediately redispatching a job held by a separate CLI process.
			if _, delayErr := pool.Exec(ctx, `UPDATE ops.executions SET available_at=GREATEST(available_at,clock_timestamp()+interval '1 second') WHERE id=$1 AND status='queued'`, id); delayErr != nil {
				return run, executionError("defer busy execution", delayErr)
			}
			run.Status = "queued"
		}
		return run, err
	}
	var scheduledAt time.Time
	var number, maxAttempts int
	var initial, maximum time.Duration
	// Keep parent cancellation from interrupting the claim transaction. Afterwards,
	// canceled work follows the normal bounded attempt-finalization path.
	claimCtx, cancelClaim := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	err = pgx.BeginFunc(claimCtx, pool, func(tx pgx.Tx) error {
		var status string
		var due, cancelled bool
		err := tx.QueryRow(claimCtx, `SELECT job_ref,scheduled_at,status,available_at<=now(),max_attempts,retry_initial_ns,retry_max_ns,cancel_requested_at IS NOT NULL FROM ops.executions WHERE id=$1 FOR UPDATE`, id).Scan(&run.Job, &scheduledAt, &status, &due, &maxAttempts, &initial, &maximum, &cancelled)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("execution not found")
		}
		if err != nil {
			return executionError("read execution", err)
		}
		if status != "queued" {
			run.Status = status
			return errors.New("execution is not queued")
		}
		var paused bool
		if err := tx.QueryRow(claimCtx, `SELECT COALESCE((SELECT paused FROM ops.job_overrides WHERE job_ref=$1),false)`, run.Job).Scan(&paused); err != nil {
			return err
		}
		if cancelled || paused {
			run.Status = "skipped"
			reason := "job paused"
			if cancelled {
				run.Status = "interrupted"
				reason = "operator interrupted"
			}
			_, err := tx.Exec(claimCtx, `UPDATE ops.executions SET status=$2,reason=$3,finished_at=clock_timestamp() WHERE id=$1`, id, run.Status, reason)
			return err
		}
		if !due {
			run.Status = "queued"
			return errors.New("execution retry is not due")
		}
		if err := tx.QueryRow(claimCtx, `SELECT COALESCE(max(number),0)+1 FROM ops.attempts WHERE execution_id=$1`, id).Scan(&number); err != nil {
			return executionError("read attempt number", err)
		}
		if number > maxAttempts {
			return errors.New("execution exhausted its attempt limit")
		}
		run.AttemptID = ulid.Make().String()
		if _, err := tx.Exec(claimCtx, `INSERT INTO ops.attempts(id,execution_id,number,status) VALUES($1,$2,$3,'running')`, run.AttemptID, id, number); err != nil {
			return executionError("start attempt", err)
		}
		_, err = tx.Exec(claimCtx, `UPDATE ops.executions SET status='running',started_at=COALESCE(started_at,now()),finished_at=NULL WHERE id=$1`, id)
		return executionError("start execution", err)
	})
	cancelClaim()
	if err != nil {
		return run, executionError("claim execution", err)
	}
	if run.Status == "interrupted" {
		return run, ErrOperatorCancelled
	}
	if run.Status == "skipped" {
		return run, fmt.Errorf("%w: job is paused", audit.ErrRefused)
	}
	ctx, stopWatch := watchExecution(ctx, locks, id)
	defer stopWatch()
	run.Status = "running"
	result, stdout, stderr, workErr := executeAttempt(ctx, pool, cfg, root, run, scheduledAt)
	workErr = executionError("run workload", workErr)
	attemptStatus := "succeeded"
	run.Status = "succeeded"
	if workErr != nil {
		attemptStatus = "failed"
		run.Status = "failed"
		if number < maxAttempts {
			run.Status = "queued"
		}
	}
	if ctx.Err() != nil {
		workErr = context.Cause(ctx)
		if errors.Is(workErr, errLockLost) {
			attemptStatus = "failed"
			run.Status = "failed"
			if number < maxAttempts {
				run.Status = "queued"
			}
		} else {
			attemptStatus = "interrupted"
			run.Status = "interrupted"
		}
	}
	var errorText *string
	var resultJSON []byte
	if workErr != nil {
		message := workErr.Error()
		errorText = &message
		resultJSON, _ = json.Marshal(map[string]any{"error": map[string]string{"code": attemptStatus, "message": message}})
	} else {
		resultJSON, err = json.Marshal(result)
		if err != nil {
			return run, errors.New("encode job result")
		}
	}
	available := time.Now().UTC()
	if run.Status == "queued" {
		available = available.Add(retryDelay(number, initial, maximum))
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = pgx.BeginFunc(finish, pool, func(tx pgx.Tx) error {
		var currentStatus string
		var cancelled bool
		if err := tx.QueryRow(finish, `SELECT status,cancel_requested_at IS NOT NULL FROM ops.executions WHERE id=$1 FOR UPDATE`, id).Scan(&currentStatus, &cancelled); err != nil {
			return err
		}
		var ownsAttempt bool
		if err := tx.QueryRow(finish, `SELECT EXISTS(SELECT FROM ops.attempts WHERE id=$1 AND status='running')`, run.AttemptID).Scan(&ownsAttempt); err != nil {
			return err
		}
		if currentStatus != "running" || !ownsAttempt {
			return errors.New("attempt no longer owns the execution")
		}

		if cancelled {
			workErr = ErrOperatorCancelled
			attemptStatus = "interrupted"
			run.Status = "interrupted"
			message := workErr.Error()
			errorText = &message
			resultJSON, _ = json.Marshal(map[string]any{"error": map[string]string{"code": "interrupted", "message": message}})
		}

		if _, err := tx.Exec(finish, `UPDATE ops.attempts SET finished_at=now(),status=$2,stdout=$3,stderr=$4,result=$5,error=$6 WHERE id=$1`, run.AttemptID, attemptStatus, stdout, stderr, resultJSON, errorText); err != nil {
			return executionError("finish attempt", err)
		}
		if _, err := tx.Exec(finish, `UPDATE ops.executions SET status=$2,finished_at=CASE WHEN $2='queued' THEN NULL ELSE now() END,available_at=$3 WHERE id=$1`, id, run.Status, available); err != nil {
			return executionError("finish execution", err)
		}
		if attemptStatus == "succeeded" && len(result.Watermark) > 0 && string(result.Watermark) != "null" {
			_, err := tx.Exec(finish, `INSERT INTO ops.watermarks(job_ref,value) VALUES($1,$2) ON CONFLICT(job_ref) DO UPDATE SET value=EXCLUDED.value,updated_at=now()`, run.Job, result.Watermark)
			return executionError("advance watermark", err)
		}
		return nil
	})
	if err != nil {
		run.Status = "running"
		return run, executionError("record job outcome", err)
	}
	return run, workErr
}

func interruptQueued(parent context.Context, pool *pgxpool.Pool, run Execution) (Execution, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	tag, err := pool.Exec(ctx, `UPDATE ops.executions SET status='interrupted',finished_at=now() WHERE id=$1 AND status='queued'`, run.ID)
	if err != nil {
		return run, executionError("interrupt queued execution", err)
	}
	if tag.RowsAffected() == 1 {
		run.Status = "interrupted"
	}
	return run, parent.Err()
}

func jobDefinition(cfg *config.Config, ref string) (config.Job, time.Duration, error) {
	if cfg == nil {
		return config.Job{}, 0, errors.New("job config is required")
	}
	job, exists := Definitions(cfg)[ref]
	if !exists {
		return job, 0, fmt.Errorf("unknown job %q", ref)
	}
	if err := job.ValidateIdempotency(); err != nil {
		return job, 0, err
	}
	if !IsSystem(ref) && (job.Python == "") == (job.Model == "") {
		return job, 0, fmt.Errorf("%s must have exactly one of python or model", ref)
	}
	if job.Model != "" {
		name, typed := strings.CutPrefix(job.Model, "model/")
		if _, exists := cfg.Models[name]; !typed || !exists {
			return job, 0, fmt.Errorf("%s references unknown model %q", ref, job.Model)
		}
	}
	if job.Python != "" {
		for _, read := range job.Reads {
			name, ok := strings.CutPrefix(read, "integration/")
			if !ok {
				continue
			}
			integration, exists := cfg.Integrations[name]
			if !exists {
				return job, 0, fmt.Errorf("unknown integration %q", name)
			}
			if integration.Auth != nil {
				switch integration.Auth.Secret {
				case "PATH", "DATABASE_URL", "PYTHONUNBUFFERED", "PYTHONDONTWRITEBYTECODE", "LANG",
					"BACKUP_DATABASE_URL", "BACKUP_ACCESS_KEY_ID", "BACKUP_SECRET_ACCESS_KEY", "BACKUP_AGE_IDENTITY":
					return job, 0, fmt.Errorf("integration secret %s conflicts with the job runtime", integration.Auth.Secret)
				}
			}
		}
	}
	timeout := cfg.Scheduler.Defaults.Timeout
	if job.Timeout != "" {
		timeout = job.Timeout
	}
	duration, err := time.ParseDuration(timeout)
	if err != nil || duration <= 0 {
		return job, 0, errors.New("invalid job timeout")
	}
	return job, duration, nil
}

func retryDelay(attempt int, initial, maximum time.Duration) time.Duration {
	delay := initial
	for n := 1; n < attempt && delay < maximum; n++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		delay = maximum
	}
	// Equal jitter avoids synchronized retries while keeping a positive delay.
	half := delay / 2
	return half + time.Duration(rand.Int64N(int64(delay-half))) + 1
}

func executionError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		if pe.Code == "42501" {
			return fmt.Errorf("%w: database role lacks permission", audit.ErrRefused)
		}
		return fmt.Errorf("%s: postgres %s", operation, pe.Code)
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return fmt.Errorf("%s: database connection failed", operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
