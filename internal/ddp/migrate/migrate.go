package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const advisoryLockKey int64 = 0x4a414b52

var migrationName = regexp.MustCompile(`^[0-9]{14}_[a-z0-9][a-z0-9_]*\.sql$`)

type StatusEntry struct {
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Checksum  string    `json:"checksum"`
	AppliedAt time.Time `json:"applied_at"`
	Applied   bool      `json:"applied"`
}

type migration struct {
	kind, id, checksum string
	sql                []byte
}

func Up(ctx context.Context, conn *pgx.Conn) error { return databaseError(run(ctx, conn)) }

// Status checks the migrations embedded in this binary. Later applied migrations
// remain in the database but are outside this binary's checksum inventory.
func Status(ctx context.Context, conn *pgx.Conn) ([]StatusEntry, error) {
	if err := validate(migrations.FS); err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	return status(ctx, tx)
}

func run(ctx context.Context, conn *pgx.Conn) error {
	if err := validate(migrations.FS); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockKey) //nolint:errcheck
	if err := ensureLedgers(ctx, conn); err != nil {
		return err
	}
	entries, err := status(ctx, conn)
	if err != nil {
		return err
	}
	byKindID := make(map[string]StatusEntry, len(entries))
	for _, e := range entries {
		byKindID[e.Kind+"/"+e.ID] = e
	}
	var pending []migration
	for _, m := range all(migrations.FS) {
		if e, ok := byKindID[m.kind+"/"+m.id]; ok {
			if e.Applied {
				continue
			}
		}
		pending = append(pending, m)
	}
	return applyPending(ctx, conn, pending)
}

func applyPending(ctx context.Context, conn *pgx.Conn, pending []migration) error {
	for len(pending) > 0 {
		applied := 0
		current := pending[0]
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			for _, m := range pending {
				current = m
				if err := applySQL(ctx, tx, m); err != nil {
					return err
				}
				applied++
				var ready bool
				if err := tx.QueryRow(ctx, "SELECT to_regclass('ddp.audit') IS NOT NULL").Scan(&ready); err != nil {
					return err
				}
				if !ready {
					// First installation keeps the bootstrap chain in one transaction
					// until it creates the audit table. Later migrations commit singly.
					continue
				}
				for _, recorded := range pending[:applied] {
					if err := audit.Record(ctx, tx, "migrate.up", "migration/"+recorded.kind+"/"+recorded.id, map[string]string{"status": "applied", "checksum": recorded.checksum}); err != nil {
						return err
					}
				}
				return nil
			}
			return errors.New("migration chain did not create the audit table")
		})
		if err != nil {
			failure := databaseError(err)
			if recordErr := recordFailure(ctx, conn, current); recordErr != nil {
				failure = errors.Join(failure, fmt.Errorf("record migration failure: %w", databaseError(recordErr)))
			}
			return fmt.Errorf("apply %s/%s: %w", current.kind, current.id, failure)
		}
		pending = pending[applied:]
	}
	return nil
}

func applySQL(ctx context.Context, tx pgx.Tx, m migration) error {
	if _, err := tx.Exec(ctx, string(m.sql)); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "INSERT INTO "+ledger(m.kind)+" (id, checksum) VALUES ($1, $2)", m.id, m.checksum)
	return err
}

func recordFailure(parent context.Context, conn *pgx.Conn, m migration) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var ready bool
		if err := tx.QueryRow(ctx, "SELECT to_regclass('ddp.audit') IS NOT NULL").Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return nil // No durable audit store exists before initial bootstrap commits.
		}
		return audit.Record(ctx, tx, "migrate.up", "migration/"+m.kind+"/"+m.id, map[string]string{"status": "failed", "checksum": m.checksum})
	})
}

func databaseError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		if pe.Code == "42501" {
			return fmt.Errorf("%w: database role lacks migration privileges", audit.ErrRefused)
		}
		return fmt.Errorf("migration database error: postgres %s", pe.Code)
	}
	return err
}

func ensureLedgers(ctx context.Context, conn *pgx.Conn) error {
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'ddp')").Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := conn.Exec(ctx, "CREATE SCHEMA ddp"); err != nil {
			return err
		}
	}
	_, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS ddp.platform_migrations (id text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS ddp.client_migrations (id text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());`)
	return err
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func status(ctx context.Context, conn queryer) ([]StatusEntry, error) {
	var platform, client bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('ddp.platform_migrations') IS NOT NULL, to_regclass('ddp.client_migrations') IS NOT NULL`).Scan(&platform, &client); err != nil {
		return nil, err
	}
	if !platform && !client {
		return pendingStatus(), nil
	}
	query := `SELECT kind, id, checksum, applied_at, true FROM (`
	if platform {
		query += `SELECT 'ddp' AS kind, id, checksum, applied_at FROM ddp.platform_migrations`
	}
	if platform && client {
		query += ` UNION ALL `
	}
	if client {
		query += `SELECT 'app', id, checksum, applied_at FROM ddp.client_migrations`
	}
	query += `) AS all_migrations ORDER BY CASE WHEN kind = 'ddp' THEN 0 ELSE 1 END, id`
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusEntry
	for rows.Next() {
		var e StatusEntry
		if err := rows.Scan(&e.Kind, &e.ID, &e.Checksum, &e.AppliedAt, &e.Applied); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	applied := make(map[string]StatusEntry, len(out))
	for _, entry := range out {
		applied[entry.Kind+"/"+entry.ID] = entry
	}
	out = nil
	latest := make(map[string]string)
	for _, m := range all(migrations.FS) {
		latest[m.kind] = m.id[:14]
		key := m.kind + "/" + m.id
		entry, ok := applied[key]
		if ok {
			if entry.Checksum != m.checksum {
				return nil, fmt.Errorf("migration checksum drift: %s", key)
			}
			delete(applied, key)
		} else {
			entry = StatusEntry{Kind: m.kind, ID: m.id, Checksum: m.checksum}
		}
		out = append(out, entry)
	}
	for key, entry := range applied {
		if !migrationName.MatchString(entry.ID + ".sql") {
			return nil, fmt.Errorf("unknown applied migration: %s", key)
		}
		timestamp := entry.ID[:14]
		if _, err := time.Parse("20060102150405", timestamp); err != nil || timestamp <= latest[entry.Kind] {
			return nil, fmt.Errorf("unknown applied migration: %s", key)
		}
		// An older image must tolerate a newer image's forward migrations.
		// SQL compatibility still requires release review and rollback testing.
	}

	return out, nil
}

func pendingStatus() []StatusEntry {
	var out []StatusEntry
	for _, m := range all(migrations.FS) {
		out = append(out, StatusEntry{Kind: m.kind, ID: m.id, Checksum: m.checksum})
	}
	return out
}

func validate(fsys fs.FS) error {
	seenTimestamps := make(map[string]string)
	for _, kind := range []string{"ddp", "app"} {
		var prev string
		entries, err := fs.ReadDir(fsys, kind)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !migrationName.MatchString(entry.Name()) {
				return fmt.Errorf("invalid %s migration filename %q", kind, entry.Name())
			}
			if prev >= entry.Name() {
				return fmt.Errorf("%s migrations are not strictly sortable", kind)
			}
			prev = entry.Name()
			ts := entry.Name()[:14]
			if _, err := time.Parse("20060102150405", ts); err != nil {
				return fmt.Errorf("invalid %s migration timestamp %q", kind, ts)
			}
			if prior, ok := seenTimestamps[kind+"/"+ts]; ok {
				return fmt.Errorf("duplicate %s migration timestamp %s in %q and %q", kind, ts, prior, entry.Name())
			}
			seenTimestamps[kind+"/"+ts] = entry.Name()
		}
	}
	return nil
}

func all(fsys fs.FS) []migration {
	var out []migration
	for _, kind := range []string{"ddp", "app"} {
		entries, _ := fs.ReadDir(fsys, kind)
		for _, e := range entries {
			b, _ := fs.ReadFile(fsys, path.Join(kind, e.Name()))
			sum := sha256.Sum256(b)
			out = append(out, migration{kind, strings.TrimSuffix(e.Name(), ".sql"), hex.EncodeToString(sum[:]), b})
		}
	}
	return out
}

func ledger(kind string) string {
	if kind == "ddp" {
		return "ddp.platform_migrations"
	}
	return "ddp.client_migrations"
}
