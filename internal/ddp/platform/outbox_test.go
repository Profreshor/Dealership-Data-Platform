package platform

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func platformDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	admin, err := pgx.Connect(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_platform_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(context.Background())
	})
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database, cfg.MaxConns = name, 1
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	conn, err := p.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(t.Context(), conn.Conn()); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	return p
}

func platformReadOnly(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	cfg := pool.Config()
	cfg.ConnConfig.RuntimeParams["role"] = "ddp_readonly"
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func seedOutbox(t *testing.T, pool *pgxpool.Pool, status, age string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `INSERT INTO ops.outbox
		(id,effect_key,payload_hash,template,context,sender,recipients,status,created_at)
		VALUES ($1,$2,repeat(E'\\000',32)::bytea,'alert','{}','sender@example.test',ARRAY['ops@example.test'],$3,clock_timestamp()-$4::interval)`,
		ulid.Make().String(), ulid.Make().String(), status, age)
	if err != nil {
		t.Fatal(err)
	}
}

func clearOutbox(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `DELETE FROM ops.deliveries; DELETE FROM ops.alerts; DELETE FROM ops.outbox`); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxStatesAndReadOnly(t *testing.T) {
	pool := platformDB(t)
	for _, tc := range []struct {
		name, status, age, wantState string
	}{
		{"empty", "", "0 seconds", "ok"},
		{"failed", "failed", "0 seconds", "failing"},
		{"delayed retry", "pending", "16 minutes", "failing"},
		{"stuck delivery", "delivering", "16 minutes", "failing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearOutbox(t, pool)
			if tc.status != "" {
				seedOutbox(t, pool, tc.status, tc.age)
			}
			if got := Outbox(t.Context(), pool); got.State != tc.wantState {
				t.Fatalf("state=%q result=%+v", got.State, got)
			}
		})
	}
	t.Run("expired failure is no longer actionable", func(t *testing.T) {
		clearOutbox(t, pool)
		seedOutbox(t, pool, "failed", "31 days")
		if _, err := pool.Exec(t.Context(), `UPDATE ops.outbox SET content_expired_at=clock_timestamp()`); err != nil {
			t.Fatal(err)
		}
		if got := Outbox(t.Context(), platformReadOnly(t, pool)); got.State != "ok" {
			t.Fatalf("expired failure result=%+v", got)
		}
	})
	t.Run("future is unknown", func(t *testing.T) {
		clearOutbox(t, pool)
		_, err := pool.Exec(t.Context(), `INSERT INTO ops.outbox
			(id,effect_key,payload_hash,template,context,sender,recipients,status,created_at)
			VALUES ($1,$2,repeat(E'\\000',32)::bytea,'alert','{}','sender@example.test',ARRAY['ops@example.test'],'failed',clock_timestamp()+interval '1 hour')`, ulid.Make().String(), ulid.Make().String())
		if err != nil {
			t.Fatal(err)
		}
		if got := Outbox(t.Context(), pool); got.State != "unknown" {
			t.Fatalf("state=%q result=%+v", got.State, got)
		}
	})
	t.Run("read only", func(t *testing.T) {
		clearOutbox(t, pool)
		before := Outbox(t.Context(), platformReadOnly(t, pool))
		if before.State != "ok" {
			t.Fatalf("before=%+v", before)
		}
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ops.outbox`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("outbox changed: count=%d err=%v", count, err)
		}
	})
}

func TestOutboxBacklogThreshold(t *testing.T) {
	pool := platformDB(t)
	defer clearOutbox(t, pool)
	seedOutbox(t, pool, "pending", "1 minute")
	if got := Outbox(t.Context(), pool); got.State != "ok" {
		t.Fatalf("recent state=%q result=%+v", got.State, got)
	}
	clearOutbox(t, pool)
	_, err := pool.Exec(t.Context(), `INSERT INTO ops.outbox
		(id,effect_key,payload_hash,template,context,sender,recipients,status)
		SELECT 'msg-'||n,'effect-'||n,repeat(E'\\000',32)::bytea,'alert','{}','sender@example.test',ARRAY['ops@example.test'],'pending'
		FROM generate_series(1,1000) n`)
	if err != nil {
		t.Fatal(err)
	}
	if got := Outbox(t.Context(), pool); got.State != "ok" {
		t.Fatalf("threshold state=%q result=%+v", got.State, got)
	}
	clearOutbox(t, pool)
	_, err = pool.Exec(t.Context(), `INSERT INTO ops.outbox
		(id,effect_key,payload_hash,template,context,sender,recipients,status)
		SELECT 'msg-'||n,'effect-'||n,repeat(E'\\000',32)::bytea,'alert','{}','sender@example.test',ARRAY['ops@example.test'],'pending'
		FROM generate_series(1,1001) n`)
	if err != nil {
		t.Fatal(err)
	}
	if got := Outbox(t.Context(), pool); got.State != "failing" {
		t.Fatalf("state=%q result=%+v", got.State, got)
	}
}

func TestOutboxUnavailable(t *testing.T) {
	p := platformDB(t)
	p.Close()
	if got := Outbox(t.Context(), p); got.State != "unknown" || got.Message == "" {
		t.Fatalf("closed pool result=%+v", got)
	}
	p = platformDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := Outbox(ctx, p); got.State != "unknown" {
		t.Fatalf("cancelled result=%+v", got)
	}
}
