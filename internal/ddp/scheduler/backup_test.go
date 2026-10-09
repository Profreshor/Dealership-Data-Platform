package scheduler

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/buildinfo"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

func TestSchedulerBackupRunsWithDueGuard(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	// Fallback roots are process-global and can only be installed once.
	if os.Getenv("DDP_BACKUP_TEST_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSchedulerBackupRunsWithDueGuard$", "-test.v")
		cmd.Env = append(os.Environ(), "DDP_BACKUP_TEST_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated scheduler backup: %v\n%s", err, output)
		}
		return
	}
	pool := schedulerDB(t)
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Fatal("pg_dump is not installed")
	}
	ctx := t.Context()
	raw := os.Getenv("TEST_DATABASE_URL")
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	db := pool.Config().ConnConfig.Database
	dbURL, _ := url.Parse(raw)
	dbURL.Path = "/" + db
	login, cleanup := schedulerBackupLogin(t, admin, dbURL)
	defer cleanup()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	server, objects, mu := schedulerBackupBucket(t)
	defer server.Close()
	cfg := schedulerConfig()
	cfg.Ddp = config.Identity{Name: "scheduler_backup", Timezone: "UTC"}
	cfg.Deploy.Backup = &config.Backup{Endpoint: server.URL, Region: "auto", Bucket: "test-backups", Recipient: identity.Recipient().String(), Interval: "1m"}
	t.Setenv("BACKUP_DATABASE_URL", login)
	t.Setenv("BACKUP_ACCESS_KEY_ID", "fixture")
	t.Setenv("BACKUP_SECRET_ACCESS_KEY", "fixture")
	t.Setenv("DDP_IMAGE_DIGEST", "ghcr.io/example/scheduler-backup@sha256:"+strings.Repeat("a", 64))
	previous := buildinfo.Version
	buildinfo.Version = strings.Repeat("b", 40)
	t.Cleanup(func() { buildinfo.Version = previous })
	poolRoots := x509.NewCertPool()
	poolRoots.AddCert(server.Certificate())
	t.Setenv("GODEBUG", "x509usefallbackroots=1")
	x509.SetFallbackRoots(poolRoots)

	now := time.Now().UTC().Truncate(time.Minute)
	stop := startScheduler(t, pool, cfg, t.TempDir())
	var id string
	waitFor(t, func() bool {
		return pool.QueryRow(ctx, "SELECT id FROM ops.executions WHERE job_ref=$1 ORDER BY id DESC LIMIT 1", jobs.BackupRef).Scan(&id) == nil
	})
	waitFor(t, func() bool {
		var n int
		return pool.QueryRow(ctx, "SELECT count(*) FROM ops.backups WHERE status='succeeded'").Scan(&n) == nil && n == 1
	})
	mu.Lock()
	manifest := 0
	archive := 0
	var manifestBody, archiveBody []byte
	for key := range objects {
		if strings.HasSuffix(key, ".json") {
			manifest++
			manifestBody = append([]byte(nil), objects[key]...)
		}
		if strings.HasSuffix(key, ".dump.age") {
			archive++
			archiveBody = bytes.Clone(objects[key])
		}
	}
	mu.Unlock()
	if manifest != 1 || archive != 1 {
		t.Fatalf("stored objects: manifest=%d archive=%d", manifest, archive)
	}
	if !bytes.HasPrefix(archiveBody, []byte("age-encryption.org/v1")) {
		t.Fatal("scheduler uploaded an unencrypted archive")
	}
	var recorded struct {
		Revision string `json:"revision"`
		Image    string `json:"image"`
	}
	if err := json.Unmarshal(manifestBody, &recorded); err != nil || recorded.Revision != strings.Repeat("b", 40) || recorded.Image != os.Getenv("DDP_IMAGE_DIGEST") {
		t.Fatalf("manifest provenance: %+v (%v)", recorded, err)
	}
	var status string
	waitFor(t, func() bool {
		return pool.QueryRow(ctx, "SELECT status FROM ops.executions WHERE id=$1", id).Scan(&status) == nil && status == "succeeded"
	})
	stop()
	var result string
	if err := pool.QueryRow(ctx, "SELECT result->'details'->'backup'->>'status' FROM ops.attempts WHERE execution_id=$1", id).Scan(&result); err != nil || result != "succeeded" {
		t.Fatalf("result=%s err=%v", result, err)
	}
	if _, err := SetPaused(ctx, pool, cfg, jobs.BackupRef, true); err != nil {
		t.Fatal(err)
	}
	if _, err := QueueRun(ctx, pool, cfg, jobs.BackupRef, false, nil); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("paused backup manual run: %v", err)
	}
	if _, err := SetPaused(ctx, pool, cfg, jobs.BackupRef, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Backfill(ctx, pool, cfg, jobs.BackupRef, now.Add(-time.Hour), now, false, nil); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("backup backfill: %v", err)
	}

	second, err := jobs.Run(ctx, pool, cfg, t.TempDir(), jobs.BackupRef)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "succeeded" {
		t.Fatalf("second execution: %+v", second)
	}
	if err := pool.QueryRow(ctx, "SELECT result->'details'->'backup'->>'status' FROM ops.attempts WHERE id=$1", second.AttemptID).Scan(&result); err != nil || result != "not_due" {
		t.Fatalf("second result=%s err=%v", result, err)
	}
	var backups int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ops.backups WHERE status='succeeded'").Scan(&backups); err != nil || backups != 1 {
		t.Fatalf("backup count=%d err=%v", backups, err)
	}
	t.Setenv("BACKUP_DATABASE_URL", "")
	failed, err := jobs.Run(ctx, pool, cfg, t.TempDir(), jobs.BackupRef)
	if err == nil || failed.Status != "failed" || !strings.Contains(err.Error(), "BACKUP_DATABASE_URL is required") || strings.Contains(err.Error(), login) {
		t.Fatalf("missing backup credential: %+v %v", failed, err)
	}
	cfg.Deploy.Backup = nil
	if job, ok := jobs.Definitions(cfg)[jobs.BackupRef]; !ok || job.Schedule != "" || job.Timeout != "2h" || job.Retry.MaxAttempts != 1 {
		t.Fatalf("removed backup configuration lost its unscheduled definition: %+v", job)
	}
}

func schedulerBackupLogin(t *testing.T, admin *pgx.Conn, db *url.URL) (string, func()) {
	t.Helper()
	name := "ddp_sched_backup_" + strings.ToLower(ulid.Make().String())
	password := "scheduler-backup-password"
	if _, err := admin.Exec(t.Context(), "CREATE ROLE "+pgx.Identifier{name}.Sanitize()+" LOGIN PASSWORD "+fmt.Sprintf("'%s'", password)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), "GRANT ddp_backup TO "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	u := *db
	u.User = url.UserPassword(name, password)
	return u.String(), func() {
		if _, err := admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error(err)
		}
	}
}

func schedulerBackupBucket(t *testing.T) (*httptest.Server, map[string][]byte, *sync.Mutex) {
	objects, mu := map[string][]byte{}, &sync.Mutex{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("list-type") == "2" {
			type object struct {
				Key string `xml:"Key"`
			}
			out := struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []object `xml:"Contents"`
			}{}
			for key := range objects {
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					out.Contents = append(out.Contents, object{key})
				}
			}
			_ = xml.NewEncoder(w).Encode(out)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/test-backups/")
		switch r.Method {
		case "PUT":
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read upload", http.StatusInternalServerError)
				return
			}
			objects[key] = b
		case "GET":
			b, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		case "DELETE":
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "bad method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server, objects, mu
}
