package migrate

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/migrations"
	"github.com/jackc/pgx/v5"
)

func TestExecutionQueueMigrationPreservesHistory(t *testing.T) {
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
	name := fmt.Sprintf("ddp_queue_migration_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	cfg := admin.Config().Copy()
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if err := ensureLedgers(ctx, conn); err != nil {
		t.Fatal(err)
	}
	legacyCount := 0
	for _, m := range all(migrations.FS) {
		if m.id < "20260904040000_execution_queue" {
			if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { return applySQL(ctx, tx, m) }); err != nil {
				t.Fatal(err)
			}
			legacyCount++
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO ops.executions(id,job_ref,scheduled_at,started_at,status) VALUES('legacy','job/example','2026-09-01 00:00:00Z','2026-09-01 00:01:00Z','succeeded');
 INSERT INTO ops.attempts(id,execution_id,started_at,status,stdout) VALUES('second','legacy','2026-09-01 00:02:00Z','succeeded','preserved second'),('first','legacy','2026-09-01 00:01:00Z','failed','preserved first')`); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var audited, invented int
	if err := conn.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE target='migration/ddp/20260904000000_baseline') FROM ddp.audit WHERE action='migrate.up'`).Scan(&audited, &invented); err != nil || audited != len(all(migrations.FS))-legacyCount || invented != 0 {
		t.Fatalf("upgrade fabricated old audit history: audited=%d invented=%d err=%v", audited, invented, err)
	}
	rows, err := conn.Query(ctx, `SELECT id,number,stdout FROM ops.attempts ORDER BY number`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id, output string
		var number int
		if err := rows.Scan(&id, &number, &output); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s:%d:%s", id, number, output))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"first:1:preserved first", "second:2:preserved second"}) {
		t.Fatal(got)
	}
	var created, scheduled time.Time
	if err := conn.QueryRow(ctx, `SELECT created_at,available_at FROM ops.executions WHERE id='legacy'`).Scan(&created, &scheduled); err != nil || created.Minute() != 1 || scheduled.Minute() != 0 {
		t.Fatalf("history times changed: %v %v %v", created, scheduled, err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO ops.attempts(id,execution_id,number,status) VALUES('duplicate','legacy',1,'failed')`); err == nil {
		t.Fatal("duplicate attempt number accepted")
	}
}
