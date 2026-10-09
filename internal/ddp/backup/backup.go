// Package backup owns encrypted off-host archives and isolated recovery checks.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

const backupLock int64 = 0x4a414b52424143

var revisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var imagePattern = regexp.MustCompile(`^ghcr\.io/[a-z0-9._/-]+@sha256:[a-f0-9]{64}$`)
var projectPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

type Manifest struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	CreatedAt time.Time `json:"created_at"`
	Revision  string    `json:"revision"`
	Image     string    `json:"image"`
	SHA256    string    `json:"sha256"`
	Bytes     int64     `json:"bytes"`
}

func prefix(project string) string     { return "backups/" + project + "/" }
func (m Manifest) objectKey() string   { return prefix(m.Project) + m.ID + ".dump.age" }
func (m Manifest) manifestKey() string { return prefix(m.Project) + m.ID + ".json" }
func (m Manifest) valid(project, id string) bool {
	_, err := ulid.ParseStrict(m.ID)
	return err == nil && m.Version == 1 && m.ID == id && m.Project == project && projectPattern.MatchString(project) && !m.CreatedAt.IsZero() && revisionPattern.MatchString(m.Revision) && imagePattern.MatchString(m.Image) && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(m.SHA256) && m.Bytes > 0 && m.Bytes < 1<<60
}

type RunOptions struct {
	Revision, Image string
	IfDue           bool
}

func deadline(ctx context.Context) time.Time {
	if end, ok := ctx.Deadline(); ok {
		return end
	}
	return time.Now().Add(2 * time.Hour)
}

type RunResult struct {
	Status  string    `json:"status"`
	Backup  *Manifest `json:"backup,omitempty"`
	Expired []string  `json:"expired"`
}

func settings(cfg *config.Config) (config.Backup, error) {
	if cfg == nil || !projectPattern.MatchString(cfg.Ddp.Name) {
		return config.Backup{}, errors.New("backup requires a valid project")
	}
	if err := config.ValidateBackup(cfg.Deploy.Backup); err != nil {
		return config.Backup{}, err
	}
	return *cfg.Deploy.Backup, nil
}

func List(ctx context.Context, cfg *config.Config) ([]Manifest, error) {
	b, err := settings(cfg)
	if err != nil {
		return nil, err
	}
	s, err := newStore(b)
	if err != nil {
		return nil, err
	}
	return list(ctx, s, cfg.Ddp.Name)
}
func list(ctx context.Context, s *store, project string) ([]Manifest, error) {
	keys, err := s.keys(ctx, prefix(project))
	if err != nil {
		return nil, err
	}
	manifests := []Manifest{}
	for _, key := range keys {
		id := strings.TrimSuffix(strings.TrimPrefix(key, prefix(project)), ".json")
		if key != prefix(project)+id+".json" {
			continue
		}
		if _, err := ulid.ParseStrict(id); err != nil {
			continue
		}
		m, err := readManifest(ctx, s, project, id)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, m)
	}
	slices.SortFunc(manifests, func(a, b Manifest) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return manifests, nil
}
func readManifest(ctx context.Context, s *store, project, id string) (Manifest, error) {
	if _, err := ulid.ParseStrict(id); err != nil {
		return Manifest{}, errors.New("backup ID must be a ULID")
	}
	body, err := s.get(ctx, prefix(project)+id+".json")
	if err != nil {
		return Manifest{}, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 65537))
	if err != nil || len(data) > 65536 {
		return Manifest{}, errors.New("read backup manifest")
	}
	var m Manifest
	if json.Unmarshal(data, &m) != nil || !m.valid(project, id) {
		return Manifest{}, errors.New("invalid backup manifest")
	}
	return m, nil
}

func Run(ctx context.Context, cfg *config.Config, raw string, options RunOptions) (RunResult, error) {
	b, err := settings(cfg)
	if err != nil {
		return RunResult{}, err
	}
	s, err := newStore(b)
	if err != nil {
		return RunResult{}, err
	}
	return run(ctx, cfg, raw, options, s)
}
func run(ctx context.Context, cfg *config.Config, raw string, options RunOptions, s *store) (result RunResult, err error) {
	b, err := settings(cfg)
	if err != nil {
		return result, err
	}
	if !revisionPattern.MatchString(options.Revision) || !imagePattern.MatchString(options.Image) {
		return result, errors.New("backup requires the exact Git revision and GHCR image digest")
	}
	u, err := connectionURL(raw, "")
	if err != nil {
		return result, err
	}
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return result, errors.New("connect to backup source")
	}
	defer conn.Close(context.Background())
	var locked bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", backupLock).Scan(&locked); err != nil || !locked {
		return result, errors.New("another backup is running or the backup lock is unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		if e := conn.QueryRow(cleanup, "SELECT pg_advisory_unlock($1)", backupLock).Scan(&released); e != nil || !released {
			err = errors.Join(err, errors.New("release backup lock"))
		}
	}()
	if options.IfDue {
		interval, _ := b.Durations()
		var due bool
		if err = conn.QueryRow(ctx, `SELECT NOT EXISTS(SELECT FROM ops.backups WHERE status='succeeded' AND started_at > clock_timestamp()-$1::interval)`, fmt.Sprintf("%f seconds", interval.Seconds())).Scan(&due); err != nil {
			return result, errors.New("read backup schedule evidence")
		}
		if !due {
			return RunResult{Status: "not_due", Expired: []string{}}, nil
		}
	}
	m := Manifest{Version: 1, ID: ulid.Make().String(), Project: cfg.Ddp.Name, CreatedAt: time.Now().UTC(), Revision: options.Revision, Image: options.Image}
	if err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO ops.backups(id,status,deadline_at) VALUES($1,'running',$2)`, m.ID, deadline(ctx)); e != nil {
			return errors.New("record backup start; maintenance privileges are required")
		}
		return audit.Record(ctx, tx, "backup.run", "backup/"+m.ID, map[string]string{"status": "running"})
	}); err != nil {
		return result, err
	}
	defer func() {
		if err == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := finishRun(cleanup, conn, m, "failed", err.Error()); e != nil {
			err = errors.Join(err, e)
		}
	}()
	dir, err := os.MkdirTemp("", "ddp-backup-")
	if err != nil {
		return result, errors.New("create backup staging directory")
	}
	defer os.RemoveAll(dir)
	file, checksum, size, err := dumpArchive(ctx, u, dir, b.Recipient)
	if err != nil {
		return result, err
	}
	defer file.Close()
	m.SHA256, m.Bytes = checksum, size
	if err = s.put(ctx, m.objectKey(), file); err != nil {
		return result, err
	}
	// Verify the stored bytes before publishing a discoverable manifest.
	remote, err := s.get(ctx, m.objectKey())
	if err != nil {
		return result, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, io.LimitReader(remote, m.Bytes+1))
	closeErr := remote.Close()
	if copyErr != nil || closeErr != nil || n != m.Bytes || fmt.Sprintf("%x", h.Sum(nil)) != m.SHA256 {
		return result, errors.New("uploaded backup integrity check failed")
	}
	encoded, _ := json.Marshal(m)
	if err = s.put(ctx, m.manifestKey(), bytes.NewReader(encoded)); err != nil {
		return result, err
	}
	result = RunResult{Status: "succeeded", Backup: &m, Expired: []string{}}
	if result.Expired, err = expire(ctx, s, conn, cfg.Ddp.Name, m.ID, b); err != nil {
		return result, err
	}
	if err = finishRun(ctx, conn, m, "succeeded", ""); err != nil {
		return result, err
	}
	return result, nil
}
func finishRun(ctx context.Context, conn *pgx.Conn, m Manifest, status, reason string) error {
	data, _ := json.Marshal(m)
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE ops.backups SET status=$2,finished_at=clock_timestamp(),manifest=$3,error=NULLIF($4,'') WHERE id=$1`, m.ID, status, string(data), reason); err != nil {
			return errors.New("record backup result")
		}
		return audit.Record(ctx, tx, "backup.run", "backup/"+m.ID, map[string]string{"status": status})
	})
}
func expire(ctx context.Context, s *store, conn *pgx.Conn, project, current string, b config.Backup) ([]string, error) {
	all, err := list(ctx, s, project)
	if err != nil {
		return nil, err
	}
	var tested string
	if err := conn.QueryRow(ctx, `SELECT COALESCE((SELECT backup_id FROM ops.backup_restores WHERE status='succeeded' ORDER BY finished_at DESC LIMIT 1),'')`).Scan(&tested); err != nil {
		return nil, errors.New("read restore retention anchor")
	}
	_, retention := b.Durations()
	cutoff := time.Now().Add(-retention)
	expired := []string{}
	listed := make(map[string]bool, len(all))
	for _, m := range all {
		listed[m.ID] = true
		if m.ID == current || m.ID == tested || !m.CreatedAt.Before(cutoff) {
			continue
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			return audit.Record(ctx, tx, "backup.expire", "backup/"+m.ID, map[string]string{"status": "requested"})
		}); err != nil {
			return expired, err
		}
		// Removing the manifest first leaves no listed backup with a missing archive.
		if err := s.delete(ctx, m.manifestKey()); err != nil {
			return expired, err
		}
		if err := s.delete(ctx, m.objectKey()); err != nil {
			return expired, err
		}
		expired = append(expired, m.ID)
	}
	// Retry unlisted archives left by a failed upload or retention deletion.
	// Only canonical IDs in this project's reserved namespace are eligible.
	keys, err := s.keys(ctx, prefix(project))
	if err != nil {
		return expired, err
	}
	for _, key := range keys {
		id := strings.TrimSuffix(strings.TrimPrefix(key, prefix(project)), ".dump.age")
		parsed, e := ulid.ParseStrict(id)
		if e != nil || key != prefix(project)+id+".dump.age" || listed[id] || id == current || id == tested || !ulid.Time(parsed.Time()).Before(cutoff) {
			continue
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			return audit.Record(ctx, tx, "backup.expire", "backup/"+id, map[string]string{"status": "retry_unlisted"})
		}); err != nil {
			return expired, err
		}
		if err := s.delete(ctx, key); err != nil {
			return expired, err
		}
		expired = append(expired, id)
	}
	return expired, nil
}
