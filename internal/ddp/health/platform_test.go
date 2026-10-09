package health

import (
	"context"
	"slices"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPlatformSchedulerObservationsAndTransitions(t *testing.T) {
	pool := healthDB(t)
	ctx := t.Context()
	cfg := &config.Config{}
	root := t.TempDir()
	evaluate := func() []Evaluation {
		t.Helper()
		values, err := Evaluate(ctx, pool, cfg, root)
		if err != nil {
			t.Fatal(err)
		}
		values = slices.DeleteFunc(values, func(e Evaluation) bool { return e.Ref == "ddp:job/ddp:cleanup" })
		if len(values) != 4 {
			t.Fatalf("platform evaluations: %+v", values)
		}
		for _, v := range values {
			if !slices.Equal(v.Notify, []string{"group/platform_ops"}) {
				t.Fatalf("platform routing: %+v", v)
			}
		}
		return values
	}
	for range 2 {
		evaluate()
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.alerts WHERE rule_ref='ddp:scheduler' AND kind='alert'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stopped alert count=%d err=%v", count, err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, platform.SchedulerLockKey); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, platform.SchedulerLockKey)
	heartbeat := func(age string) {
		t.Helper()
		_, err := conn.Exec(ctx, `INSERT INTO ops.heartbeats(service_ref,instance_id,seen_at,state) VALUES('service/scheduler','synthetic-instance',clock_timestamp()+$1::interval,'running') ON CONFLICT(service_ref) DO UPDATE SET seen_at=EXCLUDED.seen_at,state=EXCLUDED.state`, age)
		if err != nil {
			t.Fatal(err)
		}
	}
	heartbeat("0 seconds")
	for range 2 {
		evaluate()
	}
	var route []string
	if err := pool.QueryRow(ctx, `SELECT recipients FROM ops.alerts WHERE rule_ref='ddp:scheduler' AND kind='recovery'`).Scan(&route); err != nil || !slices.Equal(route, []string{"group/platform_ops"}) {
		t.Fatalf("recovery route=%v err=%v", route, err)
	}
	readCfg := pool.Config().Copy()
	readCfg.MaxConns = 1
	readCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	reader, err := pgxpool.NewWithConfig(ctx, readCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, age := range []string{"-10 seconds", "1 minute", "0 seconds"} {
		heartbeat(age)
		fact, err := platform.ReadScheduler(ctx, reader)
		check := platform.Scheduler(ctx, reader)
		if err != nil {
			t.Fatal(err)
		}
		want := "failing"
		if age == "0 seconds" {
			want = "ok"
		}
		if check.State != want || (fact.State == "running") != (want == "ok") {
			t.Fatalf("age=%s fact=%+v check=%+v", age, fact, check)
		}
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, platform.SchedulerLockKey); err != nil {
		t.Fatal(err)
	}
	if got := platform.Scheduler(ctx, reader); got.State != "failing" {
		t.Fatalf("unlocked: %+v", got)
	}
	if got := platform.Database(ctx, reader); got.State != "ok" {
		t.Fatalf("reachable: %+v", got)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := platform.Database(cancelled, reader); got.State != "unknown" {
		t.Fatalf("cancelled: %+v", got)
	}
	if got := platform.Database(ctx, nil); got.State != "unknown" {
		t.Fatalf("nil database: %+v", got)
	}
	reader.Close()
	if got := platform.Scheduler(ctx, reader); got.State != "unknown" {
		t.Fatalf("closed pool: %+v", got)
	}
}
