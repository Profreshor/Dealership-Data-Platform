package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestEncryptedBackupRecoveryAgainstPostgres(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	name := "ddp_backup_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	u, _ := url.Parse(raw)
	u.Path = "/" + name
	source, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close(context.Background())
	if err = migrate.Up(ctx, source); err != nil {
		t.Fatal(err)
	}
	backupURL, cleanupBackupLogin := backupLogin(t, admin, u)
	defer cleanupBackupLogin()
	_, err = source.Exec(ctx, `CREATE TABLE app.backup_data(id integer PRIMARY KEY,label text NOT NULL);
ALTER TABLE app.backup_data OWNER TO ddp_owner;
GRANT SELECT ON app.backup_data TO ddp_api;
INSERT INTO app.backup_data VALUES(1,'synthetic alpha'),(2,'synthetic beta');
INSERT INTO app.users(id,email,password_hash) VALUES('backup-user','backup@example.test','synthetic-hash')`)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tables = map[string]config.Table{"app.backup_data": {Purpose: "Restore fixture", Contract: config.Contract{Columns: map[string]config.Column{"id": {Type: "integer"}, "label": {Type: "text"}}, PrimaryKey: []string{"id"}}}}
	cfg.Endpoints = map[string]config.Endpoint{"backup_data": {Reads: []string{"table/app.backup_data"}, Path: "/api/backup-data", Policy: "admin", Columns: []string{"id", "label"}, UniqueKey: []string{"id"}}}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Deploy.Backup = &config.Backup{Endpoint: "https://storage.example.test", Region: "auto", Bucket: "test-backups", Recipient: key.Recipient().String()}
	t.Setenv("BACKUP_AGE_IDENTITY", key.String())
	s, objects, mu := testBucket(t)
	opts := RunOptions{Revision: strings.Repeat("a", 40), Image: "ghcr.io/test/client@sha256:" + strings.Repeat("1", 64)}
	result, err := run(ctx, cfg, backupURL, opts, s)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.Backup == nil {
		t.Fatalf("backup: %+v", result)
	}
	m := *result.Backup
	mu.Lock()
	encrypted := bytes.Clone(objects[m.objectKey()])
	mu.Unlock()
	if !bytes.HasPrefix(encrypted, []byte("age-encryption.org/v1")) {
		t.Fatal("stored archive is not age encrypted")
	}
	all, err := list(ctx, s, cfg.Ddp.Name)
	if err != nil || len(all) != 1 || all[0] != m {
		t.Fatalf("manifest roundtrip: %+v %v", all, err)
	}
	opts.IfDue = true
	if result, err = run(ctx, cfg, backupURL, opts, s); err != nil || result.Status != "not_due" {
		t.Fatalf("backup due guard: %+v %v", result, err)
	}
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if got := platform.Backup(ctx, pool, *cfg.Deploy.Backup); got.State != "failing" {
		t.Fatalf("untested backup healthy: %+v", got)
	}
	restored, err := restore(ctx, cfg, u.String(), RestoreOptions{ID: "latest", Verify: true}, s)
	if err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = admin.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_database WHERE datname=$1)`, restored.Database).Scan(&exists); err != nil || exists {
		t.Fatalf("disposable target remains: %v %v", exists, err)
	}
	if got := platform.Backup(ctx, pool, *cfg.Deploy.Backup); got.State != "ok" {
		t.Fatalf("verified backup unhealthy: %+v", got)
	}
	if got, err := restore(ctx, cfg, u.String(), RestoreOptions{ID: "latest", Verify: true, IfDue: true}, s); err != nil || got.Status != "not_due" {
		t.Fatalf("restore due guard: %+v %v", got, err)
	}
	if _, err = restore(ctx, cfg, u.String(), RestoreOptions{ID: m.ID, Database: name}, s); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing target: %v", err)
	}
	// Corruption and wrong decryption keys fail before CREATE DATABASE.
	target := "ddp_recovered_" + strings.ToLower(ulid.Make().String())
	defer admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{target}.Sanitize()+" WITH (FORCE)")
	mu.Lock()
	objects[m.objectKey()] = append([]byte{}, encrypted[:len(encrypted)-1]...)
	mu.Unlock()
	if _, err = restore(ctx, cfg, u.String(), RestoreOptions{ID: m.ID, Database: target}, s); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("corruption accepted: %v", err)
	}
	mu.Lock()
	objects[m.objectKey()] = encrypted
	mu.Unlock()
	otherKey, _ := age.GenerateX25519Identity()
	t.Setenv("BACKUP_AGE_IDENTITY", otherKey.String())
	if _, err = restore(ctx, cfg, u.String(), RestoreOptions{ID: m.ID, Database: target}, s); err == nil || !strings.Contains(err.Error(), "decryption") {
		t.Fatalf("wrong key accepted: %v", err)
	}
	t.Setenv("BACKUP_AGE_IDENTITY", key.String())
	if err = admin.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_database WHERE datname=$1)`, target).Scan(&exists); err != nil || exists {
		t.Fatal("failed archive check created target")
	}
	// Disaster recovery does not depend on the source database still existing.
	absent := *u
	absent.Path = "/ddp_absent_" + strings.ToLower(ulid.Make().String())
	if _, err = restore(ctx, cfg, absent.String(), RestoreOptions{ID: m.ID, Database: target}, s); err != nil {
		t.Fatal(err)
	}
	recoveredURL := *u
	recoveredURL.Path = "/" + target
	recovered, err := pgx.Connect(ctx, recoveredURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(context.Background())
	var rows int
	var owner string
	var grant bool
	if err = recovered.QueryRow(ctx, `SELECT count(*) FROM app.backup_data`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("restored data: %d %v", rows, err)
	}
	var email, passwordHash string
	if err = recovered.QueryRow(ctx, `SELECT email,password_hash FROM app.users WHERE id='backup-user'`).Scan(&email, &passwordHash); err != nil || email != "backup@example.test" || passwordHash != "synthetic-hash" {
		t.Fatalf("restored auth data: %s %s %v", email, passwordHash, err)
	}
	if err = recovered.QueryRow(ctx, `SELECT pg_get_userbyid(relowner),has_table_privilege('ddp_api','app.backup_data','SELECT') FROM pg_class WHERE oid='app.backup_data'::regclass`).Scan(&owner, &grant); err != nil || owner != "ddp_owner" || !grant {
		t.Fatalf("restored grants: %s %v %v", owner, grant, err)
	}
	recoveredPool, err := pgxpool.New(ctx, recoveredURL.String())
	if err != nil {
		t.Fatal(err)
	}
	if got := platform.Backup(ctx, recoveredPool, *cfg.Deploy.Backup); got.State != "ok" {
		t.Fatalf("recovered evidence incomplete: %+v", got)
	}
	recoveredPool.Close()
	if err = source.QueryRow(ctx, `SELECT count(*) FROM app.backup_data`).Scan(&rows); err != nil || rows != 2 {
		t.Fatal("source business data changed")
	}
	// Keep the last tested archive even beyond retention; expire another old one.
	old := m
	old.ID = ulid.Make().String()
	old.CreatedAt = time.Now().Add(-400 * time.Hour)
	oldData, _ := json.Marshal(old)
	mu.Lock()
	objects[old.manifestKey()] = oldData
	objects[old.objectKey()] = encrypted
	tested := m
	tested.CreatedAt = old.CreatedAt
	testedData, _ := json.Marshal(tested)
	objects[m.manifestKey()] = testedData
	mu.Unlock()
	orphan := ulid.MustNew(ulid.Timestamp(time.Now().Add(-400*time.Hour)), rand.Reader).String()
	mu.Lock()
	objects[prefix(cfg.Ddp.Name)+orphan+".dump.age"] = encrypted
	mu.Unlock()
	opts.IfDue = false
	result, err = run(ctx, cfg, backupURL, opts, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Expired) != 2 || result.Expired[0] != old.ID || result.Expired[1] != orphan {
		t.Fatalf("retention: %+v", result)
	}
	mu.Lock()
	_, kept := objects[m.objectKey()]
	_, deleted := objects[old.objectKey()]
	mu.Unlock()
	if !kept || deleted {
		t.Fatal("retention lost restore anchor or kept expired archive")
	}
	// Insufficient source privileges fail before exporting data.
	var write bool
	if err = source.QueryRow(ctx, `SELECT has_table_privilege('ddp_api','ops.backups','INSERT')`).Scan(&write); err != nil || write {
		t.Fatal("API can write maintenance evidence")
	}
	// A failed dump cannot publish a manifest or report a healthy backup.
	bin := t.TempDir()
	if err := os.WriteFile(bin+"/pg_dump", []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	before, err := list(ctx, s, cfg.Ddp.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, cfg, backupURL, opts, s); err == nil || !strings.Contains(err.Error(), "pg_dump failed") {
		t.Fatalf("dump failure accepted: %v", err)
	}
	after, err := list(ctx, s, cfg.Ddp.Name)
	if err != nil || len(before) != len(after) {
		t.Fatal("failed dump published a manifest", err)
	}
	var status string
	if err := source.QueryRow(ctx, `SELECT status FROM ops.backups ORDER BY started_at DESC LIMIT 1`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("missing failure evidence: %s %v", status, err)
	}
	if got := platform.Backup(ctx, pool, *cfg.Deploy.Backup); got.State != "failing" {
		t.Fatalf("latest backup failure is healthy: %+v", got)
	}
	if _, err := source.Exec(ctx, `UPDATE ops.backups SET status='running',deadline_at=clock_timestamp()-interval '1 second' WHERE id=(SELECT id FROM ops.backups ORDER BY started_at DESC LIMIT 1);
UPDATE ops.backup_restores SET status='succeeded',finished_at=clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	if got := platform.Backup(ctx, pool, *cfg.Deploy.Backup); got.State != "failing" {
		t.Fatal("expired maintenance deadline accepted")
	}
	if _, err := source.Exec(ctx, `UPDATE ops.backups SET deadline_at=clock_timestamp()+interval '3 hours' WHERE status='running'`); err != nil {
		t.Fatal(err)
	}
	if got := platform.Backup(ctx, pool, *cfg.Deploy.Backup); got.State != "ok" {
		t.Fatal("active maintenance was mistaken for an expired attempt")
	}
	if _, err := source.Exec(ctx, `UPDATE ops.backup_restores SET finished_at=clock_timestamp()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if got := platform.Backup(ctx, pool, *cfg.Deploy.Backup); got.State != "failing" {
		t.Fatal("stale restore evidence accepted")
	}
}

func testBucket(t *testing.T) (*store, map[string][]byte, *sync.Mutex) {
	t.Helper()
	objects := map[string][]byte{}
	mu := &sync.Mutex{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("list-type") == "2" {
			type object struct {
				Key string `xml:"Key"`
			}
			out := struct {
				XMLName   xml.Name `xml:"ListBucketResult"`
				Truncated bool     `xml:"IsTruncated"`
				Contents  []object `xml:"Contents"`
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
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			objects[key] = data
			w.Header().Set("ETag", `"fixture"`)
		case "GET":
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
		case "DELETE":
			delete(objects, key)
			w.WriteHeader(204)
		default:
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	client := s3.New(s3.Options{Region: "auto", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, HTTPClient: server.Client(), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
	})})
	return &store{client: client, transfer: transfermanager.New(client, func(o *transfermanager.Options) {
		o.Concurrency = 1
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	}), bucket: "test-backups"}, objects, mu
}
