package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestBootstrapAuditAgainstPostgres(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_auth_audit_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = admin.Close(cleanup)
	})

	config, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Database = name
	owner, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	conn, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()

	rolePool := func(role string) *pgxpool.Pool {
		cfg := config.Copy()
		cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
			_, err := c.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
			return err
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}

	api := rolePool("ddp_api")
	service := New(api, time.Hour)
	if err = service.Bootstrap(ctx, "Admin@Example.test", "admin-password"); err != nil {
		t.Fatal(err)
	}
	var id, action, target, principal string
	var outcome []byte
	if err = owner.QueryRow(ctx, `SELECT id,principal,action,target,outcome FROM ddp.audit WHERE action='users.bootstrap'`).Scan(&id, &principal, &action, &target, &outcome); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err = json.Unmarshal(outcome, &got); err != nil || len(got) != 1 || got["status"] != "created" {
		t.Fatalf("unexpected bootstrap outcome: %s", outcome)
	}
	var userCount int
	if err = owner.QueryRow(ctx, "SELECT count(*) FROM app.users WHERE id=$1", strings.TrimPrefix(target, "user/")).Scan(&userCount); err != nil || userCount != 1 {
		t.Fatalf("bootstrap user count=%d: %v", userCount, err)
	}
	var sessionPrincipal string
	if err := api.QueryRow(ctx, "SELECT session_user").Scan(&sessionPrincipal); err != nil {
		t.Fatal(err)
	}
	if action != "users.bootstrap" || id == "" || !strings.HasPrefix(target, "user/") || principal != sessionPrincipal {
		t.Fatalf("unexpected audit row: id=%q principal=%q action=%q target=%q", id, principal, action, target)
	}
	for _, sql := range []string{
		`INSERT INTO ddp.audit(id,principal,action,target,outcome) VALUES('forged','someone','users.bootstrap','user/forged','{}')`,
		`INSERT INTO ddp.audit(id,occurred_at,action,target,outcome) VALUES('forged',now(),'users.bootstrap','user/forged','{}')`,
		`UPDATE ddp.audit SET outcome='{}' WHERE action='users.bootstrap'`,
		`DELETE FROM ddp.audit WHERE action='users.bootstrap'`,
	} {
		_, err := api.Exec(ctx, sql)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatalf("API could forge or change audit metadata: %v", err)
		}
	}
	if err = service.Bootstrap(ctx, "other@example.test", "other-password"); err == nil {
		t.Fatal("repeat bootstrap succeeded")
	}
	var auditCount int
	if err = owner.QueryRow(ctx, "SELECT count(*) FROM ddp.audit WHERE action='users.bootstrap'").Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("bootstrap audit count=%d: %v", auditCount, err)
	}

	readonly := rolePool("ddp_readonly")
	if err = New(readonly, time.Hour).Bootstrap(ctx, "readonly@example.test", "readonly-password"); !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("readonly bootstrap error=%v, want audit.ErrRefused", err)
	}

}
