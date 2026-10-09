// Package deploy checks release candidates and records host deployment outcomes.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"
)

var (
	repositoryPattern = regexp.MustCompile(`^ghcr\.io/[a-z0-9._/-]+$`)
	digestPattern     = regexp.MustCompile(`^ghcr\.io/[a-z0-9._/-]+@sha256:[a-f0-9]{64}$`)
	revisionPattern   = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

type PlanResult struct {
	Action            string                `json:"action"`
	Image             string                `json:"image"`
	Current           string                `json:"current"`
	Revision          string                `json:"revision"`
	RequiresBackup    bool                  `json:"requires_backup"`
	PendingMigrations []migrate.StatusEntry `json:"pending_migrations"`
}

// Plan validates a candidate and inspects migration state without writing.
func Plan(ctx context.Context, conn *pgx.Conn, cfg *config.Config, current, image, failed, revision string) (PlanResult, error) {
	result := PlanResult{Image: image, Current: current, Revision: revision, PendingMigrations: []migrate.StatusEntry{}}
	if conn == nil || cfg == nil {
		return result, errors.New("deploy plan requires a database connection and config")
	}
	if !repositoryPattern.MatchString(cfg.Deploy.Image) {
		return result, errors.New("deploy image repository is invalid")
	}
	if !digestPattern.MatchString(current) || !sameRepository(cfg.Deploy.Image, current) {
		return result, errors.New("current image must be an exact GHCR digest for the configured repository")
	}
	if !digestPattern.MatchString(image) || !sameRepository(cfg.Deploy.Image, image) {
		return result, errors.New("image must be an exact GHCR digest for the configured repository")
	}
	if failed != "" && (!digestPattern.MatchString(failed) || !sameRepository(cfg.Deploy.Image, failed)) {
		return result, errors.New("failed image must be an exact GHCR digest for the configured repository")
	}
	if !revisionPattern.MatchString(revision) {
		return result, errors.New("revision must be a non-development 40-character Git revision")
	}
	switch {
	case current == image:
		result.Action = "unchanged"
		return result, nil
	case failed == image:
		result.Action = "rejected"
		return result, nil
	}
	result.Action = "apply"
	status, err := migrate.Status(ctx, conn)
	if err != nil {
		return PlanResult{}, sanitizeError("inspect migrations", err)
	}
	for _, entry := range status {
		if !entry.Applied {
			result.PendingMigrations = append(result.PendingMigrations, entry)
		}
	}
	result.RequiresBackup = len(result.PendingMigrations) > 0
	if result.RequiresBackup && cfg.Deploy.Backup == nil {
		return PlanResult{}, errors.New("pending migrations require deploy.backup")
	}
	return result, nil
}

func sameRepository(repository, image string) bool {
	return strings.SplitN(image, "@", 2)[0] == repository
}

type RecordOptions struct {
	Image, PreviousImage, Revision, Status, Phase string
}

type RecordResult struct {
	ID            string    `json:"id"`
	Image         string    `json:"image"`
	PreviousImage string    `json:"previous_image"`
	Revision      string    `json:"revision"`
	Status        string    `json:"status"`
	Phase         string    `json:"phase"`
	FinishedAt    time.Time `json:"finished_at"`
}

var statuses = map[string]bool{"succeeded": true, "failed": true}
var phases = map[string]bool{"preflight": true, "backup": true, "migrations": true, "models": true, "startup": true, "rollback": true, "ready": true}

func Record(ctx context.Context, conn *pgx.Conn, cfg *config.Config, opts RecordOptions) (result RecordResult, err error) {
	if conn == nil || cfg == nil || !repositoryPattern.MatchString(cfg.Deploy.Image) {
		return result, errors.New("deploy record requires a valid config and database connection")
	}
	if !digestPattern.MatchString(opts.Image) || !sameRepository(cfg.Deploy.Image, opts.Image) || !digestPattern.MatchString(opts.PreviousImage) || !sameRepository(cfg.Deploy.Image, opts.PreviousImage) {
		return result, errors.New("deployment images must be exact GHCR digests for the configured repository")
	}
	if !revisionPattern.MatchString(opts.Revision) || !statuses[opts.Status] || !phases[opts.Phase] || (opts.Status == "succeeded") != (opts.Phase == "ready") {
		return result, errors.New("deployment record fields are invalid")
	}
	id := ulid.Make().String()
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO ops.deployments(id,image,previous_image,revision,status,phase) VALUES($1,$2,$3,$4,$5,$6) RETURNING finished_at`, id, opts.Image, opts.PreviousImage, opts.Revision, opts.Status, opts.Phase).Scan(&result.FinishedAt); err != nil {
			return err
		}
		return audit.Record(ctx, tx, "deploy.record", opts.Image, map[string]string{"image": opts.Image, "previous_image": opts.PreviousImage, "revision": opts.Revision, "status": opts.Status, "phase": opts.Phase})
	})
	if err != nil {
		return RecordResult{}, sanitizeError("record deployment", err)
	}
	result.ID, result.Image, result.PreviousImage, result.Revision = id, opts.Image, opts.PreviousImage, opts.Revision
	result.Status, result.Phase = opts.Status, opts.Phase
	return result, nil
}

func Latest(ctx context.Context, conn *pgx.Conn) (*RecordResult, error) {
	if conn == nil {
		return nil, errors.New("deploy latest requires a database connection")
	}
	var result RecordResult
	err := conn.QueryRow(ctx, `SELECT id,image,previous_image,revision,status,phase,finished_at FROM ops.deployments ORDER BY finished_at DESC,id DESC LIMIT 1`).Scan(&result.ID, &result.Image, &result.PreviousImage, &result.Revision, &result.Status, &result.Phase, &result.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, sanitizeError("read latest deployment", err)
	}
	return &result, nil
}

func sanitizeError(prefix string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var pe *pgconn.PgError
	if errors.Is(err, audit.ErrRefused) || (errors.As(err, &pe) && pe.Code == "42501") {
		return fmt.Errorf("%w: database role lacks deployment privileges", audit.ErrRefused)
	}
	if pe != nil {
		return fmt.Errorf("%s: postgres %s", prefix, pe.Code)
	}
	return fmt.Errorf("%s failed; inspect the corresponding maintenance command", prefix)
}
