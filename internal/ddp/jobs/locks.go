package jobs

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errJobBusy = errors.New("job concurrency key is busy")
var errExecutionBusy = errors.New("execution is already being claimed or run")

// The scheduler's in-process keys avoid unnecessary launches. These session locks
// also cover independent manual CLI processes using the same database. A non-nil
// connection must be closed even on errJobBusy: it owns the execution key while
// the caller records the deferral, excluding another caller of the same execution.
func executionLocks(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, id string) (*pgx.Conn, error) {
	var ref string
	if err := pool.QueryRow(ctx, `SELECT job_ref FROM ops.executions WHERE id=$1`, id).Scan(&ref); err != nil {
		return nil, executionError("read execution keys", err)
	}
	executionKey := "ddp:execution:" + id
	keys := []string{executionKey, "ddp:job:" + ref}
	if cfg != nil {
		if key := Definitions(cfg)[ref].ConcurrencyKey; key != "" {
			keys = append(keys, "ddp:shared:"+key)
		}
	}
	sort.Strings(keys)
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, executionError("connect job lock", err)
	}
	for _, key := range keys {
		var held bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&held); err != nil {
			closeLocks(conn)
			return nil, executionError("acquire job lock", err)
		}
		if !held {
			if key == executionKey {
				closeLocks(conn)
				return nil, errExecutionBusy
			}
			return conn, errJobBusy
		}
	}
	return conn, nil
}

func closeLocks(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

var ErrOperatorCancelled = errors.New("execution cancelled by operator")
var errLockLost = errors.New("execution lock connection lost")

// watchExecution reuses the dedicated lock session for cancellation and ownership
// checks. Stop joins the watcher before the session is closed or reused.
func watchExecution(parent context.Context, conn *pgx.Conn, id string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, stop := context.WithTimeout(ctx, 2*time.Second)
				var requested bool
				err := conn.QueryRow(check, `SELECT cancel_requested_at IS NOT NULL FROM ops.executions WHERE id=$1`, id).Scan(&requested)
				stop()
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					cancel(errLockLost)
					return
				}
				if requested {
					cancel(ErrOperatorCancelled)
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(context.Canceled); <-done }
}
