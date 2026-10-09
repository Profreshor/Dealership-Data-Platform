package platform

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Database(ctx context.Context, pool *pgxpool.Pool) Result {
	result := Result{State: "unknown", Severity: "critical", Message: "Cannot reach the configured database. Check the database service and connection settings."}
	if pool == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		return result
	}
	result.State = "ok"
	result.Message = "The configured database accepted a connection."
	return result
}
