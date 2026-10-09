package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const SchedulerLockKey int64 = 0x4a414b5253434844

type SchedulerState struct {
	InstanceID string     `json:"instance_id"`
	SeenAt     *time.Time `json:"seen_at"`
	State      string     `json:"state"`
	LockHeld   bool       `json:"lock_held"`
}

func ReadScheduler(ctx context.Context, pool *pgxpool.Pool) (SchedulerState, error) {
	state := SchedulerState{State: "stopped"}
	if pool == nil {
		return state, errors.New("scheduler observation requires database")
	}
	err := pool.QueryRow(ctx, `SELECT instance_id,seen_at,CASE WHEN state='running' AND (seen_at<clock_timestamp()-interval '5 seconds' OR seen_at>clock_timestamp()) THEN 'stale' ELSE state END FROM ops.heartbeats WHERE service_ref='service/scheduler'`).Scan(&state.InstanceID, &state.SeenAt, &state.State)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return state, schedulerError(err)
	}
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_locks WHERE locktype='advisory' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=$1::oid AND objid=$2::oid AND objsubid=1)`, uint32(SchedulerLockKey>>32), uint32(SchedulerLockKey&0xffffffff)).Scan(&state.LockHeld)
	if !state.LockHeld && state.State == "running" {
		state.State = "stale"
	}
	return state, schedulerError(err)
}

func schedulerError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "42501" {
		return fmt.Errorf("%w: scheduler observation role lacks permission", audit.ErrRefused)
	}
	return errors.New("cannot read scheduler state")
}

func Scheduler(ctx context.Context, pool *pgxpool.Pool) Result {
	result := Result{State: "unknown", Severity: "critical", Message: "Cannot read scheduler state."}
	if pool == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, err := ReadScheduler(ctx, pool)
	if err != nil {
		return result
	}
	switch state.State {
	case "running", "stopped", "stale":
	default:
		return result
	}
	result.Value = fmt.Sprintf("state=%s lock_held=%t", state.State, state.LockHeld)
	if state.State == "running" && state.LockHeld {
		result.State = "ok"
		result.Message = "Scheduler heartbeat is current and its planner lock is held."
	} else {
		result.State = "failing"
		result.Message = "Scheduler is stopped or its heartbeat or planner lock is missing. Start or inspect the scheduler process."
	}
	return result
}
