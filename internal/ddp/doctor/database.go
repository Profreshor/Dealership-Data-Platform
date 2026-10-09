package doctor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Migrations(ctx context.Context, pool *pgxpool.Pool) platform.Result {
	unknown := result("unknown", "Migration state could not be read.")
	if pool == nil {
		return unknown
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return unknown
	}
	defer conn.Release()
	entries, err := migrate.Status(ctx, conn.Conn())
	if err != nil {
		if strings.HasPrefix(err.Error(), "migration checksum drift:") || strings.HasPrefix(err.Error(), "unknown applied migration:") {
			return result("failing", "Applied migration history differs from this binary.")
		}
		return unknown
	}
	pending := 0
	for _, entry := range entries {
		if !entry.Applied {
			pending++
		}
	}
	if pending > 0 {
		return result("failing", fmt.Sprintf("%d migrations are pending in this binary.", pending))
	}
	return result("ok", "All migrations embedded in this binary are applied with matching checksums.")
}

func Clock(ctx context.Context, pool *pgxpool.Pool) platform.Result {
	if pool == nil {
		return result("unknown", "Database clock could not be compared with the operator clock.")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	var observed time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observed); err != nil {
		return result("unknown", "Database clock could not be compared with the operator clock.")
	}
	return clockResult(start, time.Now(), observed)
}

func clockResult(start, end, observed time.Time) platform.Result {
	if end.Before(start) {
		return result("unknown", "The operator clock changed during observation.")
	}
	if observed.Before(start.Add(-5*time.Second)) || observed.After(end.Add(5*time.Second)) {
		return result("failing", "Database and operator clocks differ by more than five seconds after accounting for query time.")
	}
	return result("ok", "Database clock lies within five seconds of the operator observation interval; absolute time synchronization is not proven.")
}
