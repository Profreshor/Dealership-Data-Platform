package platform

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

func TestPlatformVerificationSchedulerMissingFutureAndLock(t *testing.T) {
	pool := platformDB(t)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `DELETE FROM ops.heartbeats WHERE service_ref='service/scheduler'`); err != nil {
		t.Fatal(err)
	}
	if got := Scheduler(ctx, pool); got.State != "failing" || got.Severity != "critical" {
		t.Fatalf("missing scheduler heartbeat: %+v", got)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.heartbeats(service_ref,instance_id,seen_at,state) VALUES('service/scheduler',$1,clock_timestamp()+interval '1 minute','running')`, ulid.Make().String()); err != nil {
		t.Fatal(err)
	}
	if got := Scheduler(ctx, pool); got.State != "failing" || got.Severity != "critical" || strings.Contains(got.Message, "pq") {
		t.Fatalf("future scheduler heartbeat: %+v", got)
	}
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, SchedulerLockKey); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, SchedulerLockKey)
	if _, err := pool.Exec(ctx, `UPDATE ops.heartbeats SET seen_at=clock_timestamp(),state='running' WHERE service_ref='service/scheduler'`); err != nil {
		t.Fatal(err)
	}
	if got := Scheduler(ctx, pool); got.State != "ok" || got.Severity != "critical" {
		t.Fatalf("current heartbeat and held lock: %+v", got)
	}
}

func TestPlatformVerificationUnknownResultsAreBoundedAndPersistable(t *testing.T) {
	pool := platformDB(t)
	ctx := t.Context()
	checks := []Result{
		Database(ctx, nil),
		Scheduler(ctx, nil),
		Disk("/path/that/does/not/exist/ddp"),
		Outbox(ctx, nil),
	}
	for _, got := range checks {
		if got.State != "unknown" || (got.Severity != "warning" && got.Severity != "critical") || got.Message == "" || strings.Contains(got.Message, "pq") {
			t.Fatalf("unbounded/invalid unknown result: %+v", got)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.outbox(id,effect_key,payload_hash,template,context,sender,recipients,status,created_at) VALUES($1,$2,repeat(E'\\000',32)::bytea,'alert','{}','sender@example.test',ARRAY['ops@example.test'],'failed',clock_timestamp()+interval '1 hour')`, ulid.Make().String(), ulid.Make().String()); err != nil {
		t.Fatal(err)
	}
	if got := Outbox(ctx, pool); got.State != "unknown" || got.Severity != "warning" || got.Message == "" {
		t.Fatalf("future outbox timestamp: %+v", got)
	}
	if state, severity := diskState(0, 1); state != "failing" || severity != "critical" {
		t.Fatalf("zero available disk: %q, %q", state, severity)
	}
	if state, severity := diskState(1, 0); state != "unknown" || (severity != "warning" && severity != "critical") {
		t.Fatalf("invalid disk stat: %q, %q", state, severity)
	}
}
