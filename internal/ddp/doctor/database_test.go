package doctor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestDoctorReadOnlyDatabaseAndMigrationDrift(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_doctor_" + strings.ToLower(ulid.Make().String())
	if _, err := admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(context.Background())
	})
	pc, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if got := Migrations(t.Context(), pool); got.State != "failing" {
		t.Fatalf("empty migrations: %+v", got)
	}
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), conn.Conn()); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	if _, err := pool.Exec(t.Context(), `INSERT INTO ddp.platform_migrations(id,checksum) VALUES('20990101000000_forward','new-image');
INSERT INTO ddp.client_migrations(id,checksum) VALUES('20990101000000_forward','new-image')`); err != nil {
		t.Fatal(err)
	}
	if Migrations(t.Context(), pool).State != "ok" || Clock(t.Context(), pool).State != "ok" {
		t.Fatal("current database rejected")
	}
	root, revision := gitFixture(t)
	cfg, err := config.Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Ddp.TemplateRevision = revision
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer server.Close()
	cfg.Serving.Addr = strings.TrimPrefix(server.URL, "http://")
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "ddp.yaml")
	if err := os.WriteFile(registry, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	readerURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	readerURL.Path = "/" + name
	query := readerURL.Query()
	query.Set("dbname", name)
	query.Set("role", "ddp_readonly")
	readerURL.RawQuery = query.Encode()
	t.Setenv("DATABASE_URL", readerURL.String())
	lock, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if _, err := lock.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, platform.SchedulerLockKey); err != nil {
		t.Fatal(err)
	}
	defer lock.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, platform.SchedulerLockKey)
	if _, err := pool.Exec(t.Context(), `INSERT INTO ops.heartbeats(service_ref,instance_id,state,seen_at) VALUES('service/scheduler','fixture','running',clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	before := auditCount(t, pool)
	report := Run(t.Context(), registry)
	wantState := "ok"
	for _, check := range report.Checks {
		// This fixture controls every dependency except the host's free disk
		// space. A real low-capacity warning must still affect the report.
		if check.Ref == "ddp:disk" && check.State == "failing" {
			wantState = "failing"
			continue
		}
		if check.State != "ok" {
			t.Fatalf("controlled check is unhealthy: %+v", check)
		}
	}
	if report.State != wantState {
		t.Fatalf("report did not aggregate checks: %+v", report)
	}
	if len(report.Checks) != 13 {
		t.Fatalf("missing checks: %+v", report)
	}
	if after := auditCount(t, pool); after != before {
		t.Fatal("doctor wrote audit records")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE ddp.platform_migrations SET checksum='changed' WHERE id=(SELECT min(id) FROM ddp.platform_migrations)`); err != nil {
		t.Fatal(err)
	}
	if got := Migrations(t.Context(), pool); got.State != "failing" {
		t.Fatalf("drift: %+v", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	if Migrations(ctx, pool).State != "unknown" || Clock(ctx, pool).State != "unknown" {
		t.Fatal("cancelled checks accepted")
	}
}

func auditCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ddp.audit`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
