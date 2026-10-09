package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Backup reports persisted maintenance evidence; it never contacts storage or restores data.
func Backup(ctx context.Context, pool *pgxpool.Pool, b config.Backup) Result {
	result := Result{State: "unknown", Severity: "critical", Message: "Backup and restore evidence is unavailable."}
	if pool == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	interval, _ := b.Durations()
	var recent, restored, failed bool
	if err := pool.QueryRow(ctx, `SELECT
EXISTS(SELECT FROM ops.backups WHERE status='succeeded' AND started_at>clock_timestamp()-$1::interval),
EXISTS(SELECT FROM ops.backup_restores WHERE status='succeeded' AND finished_at>clock_timestamp()-interval '7 days'),
COALESCE((SELECT status='failed' OR (status='running' AND deadline_at<clock_timestamp()) FROM ops.backups ORDER BY started_at DESC LIMIT 1),false)
OR COALESCE((SELECT status='failed' OR (status='running' AND deadline_at<clock_timestamp()) FROM ops.backup_restores ORDER BY started_at DESC LIMIT 1),false)`, fmt.Sprintf("%f seconds", (interval+time.Hour).Seconds())).Scan(&recent, &restored, &failed); err != nil {
		return result
	}
	result.State = "failing"
	result.Message = "A verified off-host backup and a successful restore test within seven days are required."
	if recent && restored && !failed {
		result.State = "ok"
		result.Message = "Backup and restore verification evidence is current."
	}
	if failed {
		result.Message = "The latest backup or restore attempt failed or stopped reporting."
	}
	return result
}
