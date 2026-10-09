package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

type RestoreOptions struct {
	ID, Database  string
	Verify, IfDue bool
}
type RestoreResult struct {
	Version      int        `json:"version"`
	ID           string     `json:"id"`
	Project      string     `json:"project"`
	BackupID     string     `json:"backup_id"`
	Database     string     `json:"database"`
	Verification bool       `json:"verification"`
	Status       string     `json:"status"`
	StartedAt    time.Time  `json:"started_at"`
	DeadlineAt   time.Time  `json:"deadline_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func Restore(ctx context.Context, cfg *config.Config, raw string, options RestoreOptions) (RestoreResult, error) {
	b, err := settings(cfg)
	if err != nil {
		return RestoreResult{}, err
	}
	s, err := newStore(b)
	if err != nil {
		return RestoreResult{}, err
	}
	return restore(ctx, cfg, raw, options, s)
}
func restore(ctx context.Context, cfg *config.Config, raw string, options RestoreOptions, s *store) (result RestoreResult, err error) {
	if _, err = settings(cfg); err != nil {
		return result, err
	}
	if options.IfDue && !options.Verify {
		return result, errors.New("restore --if-due requires --verify")
	}
	if options.Verify && options.Database != "" {
		return result, errors.New("restore verification generates its own disposable database")
	}
	if !options.Verify && !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(options.Database) {
		return result, errors.New("restore requires a new database name (lowercase identifier, at most 63 characters)")
	}
	u, err := connectionURL(raw, "")
	if err != nil {
		return result, err
	}
	maintenanceURL, err := connectionURL(raw, "postgres")
	if err != nil {
		return result, err
	}
	maintenance, err := pgx.Connect(ctx, maintenanceURL.String())
	if err != nil {
		return result, errors.New("connect to restore maintenance database")
	}
	defer maintenance.Close(context.Background())
	// Recovery can proceed when the original database is lost. Off-host receipts
	// remain mandatory; source-database evidence is updated when available.
	source, _ := pgx.Connect(ctx, u.String())
	if source != nil {
		var ledger bool
		if e := source.QueryRow(ctx, `SELECT to_regclass('ops.backup_restores') IS NOT NULL`).Scan(&ledger); e != nil || !ledger {
			_ = source.Close(ctx)
			source = nil
		}
	}
	if source != nil {
		defer source.Close(context.Background())
	}
	if options.IfDue {
		if source == nil {
			return result, errors.New("read restore schedule evidence")
		}
		var due bool
		if e := source.QueryRow(ctx, `SELECT NOT EXISTS(SELECT FROM ops.backup_restores WHERE status='succeeded' AND finished_at>clock_timestamp()-interval '7 days')`).Scan(&due); e != nil {
			return result, errors.New("read restore schedule evidence")
		}
		if !due {
			return RestoreResult{Version: 1, Status: "not_due", Project: cfg.Ddp.Name}, nil
		}
	}
	id := options.ID
	if id == "latest" {
		manifests, e := list(ctx, s, cfg.Ddp.Name)
		if e != nil {
			return result, e
		}
		if len(manifests) == 0 {
			return result, errors.New("no verified off-host backups")
		}
		id = manifests[0].ID
	}
	m, err := readManifest(ctx, s, cfg.Ddp.Name, id)
	if err != nil {
		return result, err
	}
	target := options.Database
	if options.Verify {
		target = "ddp_restore_" + strings.ToLower(ulid.Make().String())
	}
	var exists bool
	if e := maintenance.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_database WHERE datname=$1)`, target).Scan(&exists); e != nil {
		return result, errors.New("inspect restore target")
	}
	if exists {
		return result, errors.New("restore target already exists; existing databases are never overwritten")
	}
	var roles int
	if e := maintenance.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname=ANY($1) AND NOT rolcanlogin AND NOT rolsuper`, []string{"ddp_owner", "ddp_scheduler", "ddp_job", "ddp_api", "ddp_readonly", "ddp_backup"}).Scan(&roles); e != nil || roles != 6 {
		return result, errors.New("restore requires the six provisioned NOLOGIN DDP component roles")
	}
	dir, err := os.MkdirTemp("", "ddp-restore-")
	if err != nil {
		return result, errors.New("create restore staging directory")
	}
	defer os.RemoveAll(dir)
	result = RestoreResult{Version: 1, ID: ulid.Make().String(), Project: cfg.Ddp.Name, BackupID: m.ID, Database: target, Verification: options.Verify, Status: "running", StartedAt: time.Now().UTC(), DeadlineAt: deadline(ctx)}
	if err = saveRestore(ctx, s, source, result); err != nil {
		return result, err
	}
	defer func() {
		result.Status = "succeeded"
		if err != nil {
			result.Status = "failed"
		}
		finished := time.Now().UTC()
		result.FinishedAt = &finished
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := saveRestore(cleanup, s, source, result); e != nil {
			err = errors.Join(err, e)
			result.Status = "failed"
		}
	}()
	archive, err := downloadArchive(ctx, s, m, dir)
	if err != nil {
		return result, err
	}
	if _, err = maintenance.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{target}.Sanitize()+" TEMPLATE template0"); err != nil {
		return result, errors.New("create restore database; maintenance privileges are required")
	}
	if options.Verify {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, e := maintenance.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{target}.Sanitize()+" WITH (FORCE)"); e != nil {
				err = errors.Join(err, errors.New("remove disposable restore database"))
			}
		}()
	}
	targetURL, _ := connectionURL(raw, target)
	if err = pgCommand(ctx, "pg_restore", targetURL, "--no-password", "--exit-on-error", "--single-transaction", "--dbname="+target, archive); err != nil {
		return result, err
	}
	pc, e := pgxpool.ParseConfig(targetURL.String())
	if e != nil {
		return result, errors.New("configure restored database check")
	}
	pc.ConnConfig.RuntimeParams["role"] = "ddp_api"
	pool, e := pgxpool.NewWithConfig(ctx, pc)
	if e != nil {
		return result, errors.New("connect restored database for verification")
	}
	e = verifyRestore(ctx, pool, cfg)
	pool.Close()
	if e != nil {
		return result, errors.New("restored database failed read-only migration, contract or endpoint checks")
	}
	if !options.Verify {
		restored, e := pgx.Connect(ctx, targetURL.String())
		if e != nil {
			return result, errors.New("connect restored audit store")
		}
		e = pgx.BeginFunc(ctx, restored, func(tx pgx.Tx) error {
			// The dump captured its own backup as running. Its verified manifest
			// and this completed restore provide the missing recovery evidence.
			manifest, _ := json.Marshal(m)
			if _, err := tx.Exec(ctx, `UPDATE ops.backups SET status='succeeded',finished_at=clock_timestamp(),manifest=$2,error=NULL WHERE id=$1`, m.ID, string(manifest)); err != nil {
				return errors.New("record recovered backup evidence")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO ops.backup_restores(id,backup_id,target_database,status,started_at,finished_at,deadline_at) VALUES($1,$2,$3,'succeeded',$4,clock_timestamp(),$5)`, result.ID, m.ID, target, result.StartedAt, result.DeadlineAt); err != nil {
				return errors.New("record recovered restore evidence")
			}
			return audit.Record(ctx, tx, "backup.restore", "backup/"+m.ID, map[string]string{"status": "succeeded", "database": target})
		})
		_ = restored.Close(ctx)
		if e != nil {
			return result, e
		}
	}
	return result, nil
}
func saveRestore(ctx context.Context, s *store, source *pgx.Conn, r RestoreResult) error {
	data, _ := json.Marshal(r)
	// Each stage has its own key, so a crash leaves a durable running receipt.
	key := "restores/" + r.Project + "/" + r.ID + "_" + r.Status + ".json"
	if err := s.put(ctx, key, bytes.NewReader(data)); err != nil {
		return errors.New("persist off-host restore receipt")
	}
	if source == nil {
		return nil
	}
	return pgx.BeginFunc(ctx, source, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.backup_restores(id,backup_id,target_database,status,started_at,finished_at,deadline_at) VALUES($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT(id) DO UPDATE SET status=excluded.status,finished_at=excluded.finished_at`, r.ID, r.BackupID, r.Database, r.Status, r.StartedAt, r.FinishedAt, r.DeadlineAt); err != nil {
			return errors.New("record restore result; maintenance privileges are required")
		}
		return audit.Record(ctx, tx, "backup.restore", "backup/"+r.BackupID, map[string]string{"status": r.Status, "database": r.Database})
	})
}
