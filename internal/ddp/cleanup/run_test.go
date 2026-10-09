package cleanup

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestPositiveDuration(t *testing.T) {
	for _, value := range []string{"1s", "720h", "250ms"} {
		if d, err := positiveDuration(value); err != nil || d <= 0 {
			t.Fatalf("positiveDuration(%q) = %v, %v", value, d, err)
		}
	}
	for _, value := range []string{"", "0s", "-1s", "nope"} {
		if _, err := positiveDuration(value); err == nil {
			t.Fatalf("positiveDuration(%q) accepted invalid duration", value)
		}
	}
}

func TestRunAgainstDisposablePostgres(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := "ddp_cleanup_" + strings.ToLower(ulid.Make().String())
	ident := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, dropErr := admin.Exec(context.Background(), "DROP DATABASE "+ident+" WITH (FORCE)"); dropErr != nil {
			t.Errorf("drop disposable database %s: %v", name, dropErr)
		}
		if closeErr := admin.Close(context.Background()); closeErr != nil {
			t.Errorf("close database administrator: %v", closeErr)
		}
	})

	pool := testPool(t, base, name, "")
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	scheduler := testPool(t, base, name, "ddp_scheduler")
	readonly := testPool(t, base, name, "ddp_readonly")
	cfg := &config.Config{Retention: &config.Retention{RunLogs: "1h", Health: "1h", Messages: "1h"}}
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-5 * time.Minute)

	statements := []struct {
		query string
		args  []any
	}{
		{`CREATE TABLE mart.client_rows (id integer PRIMARY KEY, secret text NOT NULL)`, nil},
		{`INSERT INTO mart.client_rows VALUES (1,'business-data')`, nil},
		{`INSERT INTO ops.executions(id,job_ref,scheduled_at,finished_at,status) VALUES
		 ('e-terminal','job/x',$1,$1,'succeeded'),('e-active-parent','job/x',$1,NULL,'running')`, []any{old}},
		{`INSERT INTO ops.attempts(id,execution_id,number,finished_at,status,stdout,stderr,result,error)
		 SELECT 'a-'||g,'e-terminal',g,$1,'succeeded','stdout','stderr',jsonb_build_object('value',g),'error' FROM generate_series(1,1005) g`, []any{old}},
		{`INSERT INTO ops.attempts(id,execution_id,number,finished_at,status,stdout,stderr,result,error) VALUES
		 ('a-active-parent','e-active-parent',1,$1,'failed','keep','keep','{"keep":true}','keep'),
		 ('a-active-attempt','e-terminal',1006,NULL,'running','keep','keep','{"keep":true}','keep')`, []any{old}},
		{`INSERT INTO ops.watermarks(job_ref,value,updated_at) VALUES ('job/x','{"cursor":"keep"}',$1)`, []any{old}},
		{`INSERT INTO ops.ticks(id,job_ref,scheduled_at,local_time,timezone,status,execution_id) VALUES ('tick-keep','job/x',$1,'old','UTC','launched','e-terminal')`, []any{old}},
		{`INSERT INTO ops.outbox(id,effect_key,payload_hash,template,context,sender,recipients,status,rendered_at,subject,text_body,html_body,finished_at) VALUES
		 ('m-delivered','effect-delivered',decode(repeat('aa',32),'hex'),'alert','{"secret":"x"}','from',ARRAY['to'],'delivered',$1,'subject','body','html',$1),
		 ('m-failed','effect-failed',decode(repeat('bb',32),'hex'),'alert','{"secret":"x"}','from',ARRAY['to'],'failed',$1,'subject','body','html',$1),
		 ('m-pending','effect-pending',decode(repeat('cc',32),'hex'),'alert','{"keep":true}','from',ARRAY['to'],'pending',$1,'subject','body','html',$1),
		 ('m-delivering','effect-delivering',decode(repeat('dd',32),'hex'),'alert','{"keep":true}','from',ARRAY['to'],'delivering',$1,'subject','body','html',$1),
		 ('m-recent','effect-recent',decode(repeat('ee',32),'hex'),'alert','{"keep":true}','from',ARRAY['to'],'delivered',$2,'subject','body','html',$2)`, []any{old, recent}},
		{`INSERT INTO ops.health_evaluations(id,rule_ref,observed_at,observation,state,severity) VALUES
		 ('h-delete','delete',$1,'{"ref":"delete","secret":"drop"}','ok','warning'),
		 ('h-expire','expire',$1,'{"ref":"expire","target":"target","secret":"drop","notify":["group/x"]}','failing','critical'),
		 ('h-current','current',$1,'{"ref":"current","secret":"keep"}','failing','critical'),
		 ('h-pending','pending',$1,'{"ref":"pending","secret":"keep"}','failing','critical')`, []any{old}},
		{`INSERT INTO ops.alerts(id,rule_ref,evaluation_id,incident_id,kind,created_at,recipients,context,message_id) VALUES
		 ('al-expire','expire','h-expire','i','alert',$1,ARRAY['to'],'{"secret":"drop"}','m-delivered'),
		 ('al-pending','pending','h-pending','i','alert',$1,ARRAY['to'],'{"secret":"keep"}',NULL)`, []any{old}},
		{`INSERT INTO ops.alert_state(rule_ref,state,evaluation_id,incident_id,recipients) VALUES ('current','failing','h-current','i',ARRAY['to'])`, nil},
		{`INSERT INTO ops.model_refreshes(id,model_ref,started_at,finished_at,status,error) VALUES
		 ('model-old','model/x',$1,$1,'failed','drop'),('model-recent','model/x',$2,$2,'failed','keep')`, []any{old, recent}},
		{`INSERT INTO app.users(id,email,password_hash) VALUES ('user-keep','user@example.test','password-hash')`, nil},
		{`INSERT INTO app.sessions(token_hash,user_id,expires_at) VALUES (decode(repeat('11',32),'hex'),'user-keep',$1),(decode(repeat('22',32),'hex'),'user-keep',$2)`, []any{old, time.Now().Add(time.Hour)}},
		{`INSERT INTO ddp.audit(id,action,target,outcome) VALUES ('audit-keep','before','fixture','{}')`, nil},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("fixture setup: %v", err)
		}
	}

	t.Run("invalid durations make no writes", func(t *testing.T) {
		before := scalar[int](t, pool, `SELECT count(*) FROM ddp.audit`)
		bad := &config.Config{Retention: &config.Retention{RunLogs: "0", Health: "1h", Messages: "1h"}}
		if _, err := Run(ctx, scheduler, bad); err == nil {
			t.Fatal("invalid duration accepted")
		}
		if after := scalar[int](t, pool, `SELECT count(*) FROM ddp.audit`); after != before {
			t.Fatalf("invalid configuration wrote audit rows: %d -> %d", before, after)
		}
	})

	t.Run("readonly role is refused without writes", func(t *testing.T) {
		before := scalar[int](t, pool, `SELECT count(*) FROM ops.attempts WHERE payload_expired_at IS NOT NULL`)
		if _, err := Run(ctx, readonly, cfg); !errors.Is(err, audit.ErrRefused) {
			t.Fatalf("readonly Run error = %v, want operation refused", err)
		}
		if after := scalar[int](t, pool, `SELECT count(*) FROM ops.attempts WHERE payload_expired_at IS NOT NULL`); after != before {
			t.Fatalf("readonly cleanup wrote rows: %d -> %d", before, after)
		}
	})

	t.Run("scheduler expires only eligible evidence", func(t *testing.T) {
		got, err := Run(ctx, scheduler, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got.RunLogs != 1005 || got.Messages != 2 || got.HealthDeleted != 1 || got.HealthExpired != 1 || got.AlertContexts != 1 || got.ModelErrors != 1 || got.Sessions != 1 || got.More {
			t.Fatalf("counts = %#v", got)
		}
		assertCount(t, pool, 1005, `SELECT count(*) FROM ops.attempts WHERE id LIKE 'a-%%' AND stdout='' AND stderr='' AND result IS NULL AND error IS NULL AND payload_expired_at IS NOT NULL`)
		assertCount(t, pool, 2, `SELECT count(*) FROM ops.attempts WHERE id IN ('a-active-parent','a-active-attempt') AND stdout='keep' AND payload_expired_at IS NULL`)
		assertCount(t, pool, 2, `SELECT count(*) FROM ops.outbox WHERE id IN ('m-delivered','m-failed') AND context='{}' AND subject IS NULL AND text_body IS NULL AND html_body IS NULL AND content_expired_at IS NOT NULL`)
		assertCount(t, pool, 3, `SELECT count(*) FROM ops.outbox WHERE id IN ('m-pending','m-delivering','m-recent') AND context->>'keep'='true' AND subject='subject' AND content_expired_at IS NULL`)
		assertCount(t, pool, 0, `SELECT count(*) FROM ops.health_evaluations WHERE id='h-delete'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ops.health_evaluations WHERE id='h-expire' AND evidence_expired_at IS NOT NULL AND observation->>'message'='Health evidence expired.' AND NOT observation ? 'secret'`)
		assertCount(t, pool, 2, `SELECT count(*) FROM ops.health_evaluations WHERE id IN ('h-current','h-pending') AND observation->>'secret'='keep' AND evidence_expired_at IS NULL`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ops.alerts WHERE id='al-expire' AND context='{}' AND message_id='m-delivered' AND evaluation_id='h-expire'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ops.alerts WHERE id='al-pending' AND context->>'secret'='keep' AND message_id IS NULL`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ops.model_refreshes WHERE id='model-old' AND error IS NULL`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ops.model_refreshes WHERE id='model-recent' AND error='keep'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM app.sessions`)
		assertCount(t, pool, 1, `SELECT count(*) FROM app.users WHERE password_hash='password-hash'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM mart.client_rows WHERE secret='business-data'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ops.executions WHERE id='e-terminal' AND status='succeeded'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ops.watermarks WHERE value->>'cursor'='keep'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ops.ticks WHERE id='tick-keep'`)
		assertCount(t, pool, 2, `SELECT count(*) FROM ops.outbox WHERE id IN ('m-delivered','m-failed') AND octet_length(payload_hash)=32 AND effect_key LIKE 'effect-%%'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ddp.audit WHERE id='audit-keep'`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ddp.audit WHERE action='cleanup.run' AND (outcome->>'run_logs')::integer=1000`)
		assertCount(t, pool, 1, `SELECT count(*) FROM ddp.audit WHERE action='cleanup.run' AND (outcome->>'run_logs')::integer=5`)

		second, err := Run(ctx, scheduler, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if second.RunLogs+second.Messages+second.HealthDeleted+second.HealthExpired+second.AlertContexts+second.ModelErrors+second.Sessions != 0 || second.More {
			t.Fatalf("repeat cleanup changed rows: %#v", second)
		}
	})

	t.Run("session function has narrow privileges", func(t *testing.T) {
		if got := scalar[int](t, scheduler, `SELECT ddp.expire_sessions()`); got != 0 {
			t.Fatalf("unexpected expired sessions: %d", got)
		}
		for _, query := range []string{`SELECT token_hash FROM app.sessions`, `SELECT password_hash FROM app.users`} {
			if _, err := scheduler.Exec(ctx, query); err == nil {
				t.Fatalf("scheduler read protected authentication data with %q", query)
			}
		}
	})

	t.Run("cancellation preserves committed counts and rolls back current batch", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `INSERT INTO ops.executions(id,job_ref,scheduled_at,finished_at,status) VALUES ('e-cancel','job/x',$1,$1,'succeeded')`, old); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO ops.attempts(id,execution_id,number,finished_at,status,stdout)
		 SELECT 'cancel-'||g,'e-cancel',g,$1,'succeeded','keep' FROM generate_series(1,1005) g`, old); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `CREATE FUNCTION ddp.block_second_cleanup_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN
		   IF NEW.action='cleanup.run' AND (NEW.outcome->>'run_logs')::integer=5 THEN
		     PERFORM pg_advisory_xact_lock(8675309);
		   END IF;
		   RETURN NEW;
		 END $$`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `CREATE TRIGGER block_second_cleanup_audit BEFORE INSERT ON ddp.audit
		 FOR EACH ROW EXECUTE FUNCTION ddp.block_second_cleanup_audit()`); err != nil {
			t.Fatal(err)
		}
		lock, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lock.Rollback(context.Background()) }()
		if _, err := lock.Exec(ctx, `SELECT pg_advisory_xact_lock(8675309)`); err != nil {
			t.Fatal(err)
		}
		cancelCtx, cancel := context.WithCancel(ctx)
		result := make(chan struct {
			counts Counts
			err    error
		}, 1)
		go func() {
			counts, runErr := Run(cancelCtx, scheduler, cfg)
			result <- struct {
				counts Counts
				err    error
			}{counts, runErr}
		}()
		blocked := false
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if scalar[int](t, pool, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory'`) > 0 {
				blocked = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		got := <-result
		if !blocked {
			t.Fatal("second batch did not reach its blocked audit insert")
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Run error = %v, want context canceled", got.err)
		}
		if got.counts != (Counts{RunLogs: 1000}) {
			t.Fatalf("canceled run counts = %#v, want 1000 committed run logs", got.counts)
		}
		if err := lock.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		assertCount(t, pool, 1000, `SELECT count(*) FROM ops.attempts WHERE id LIKE 'cancel-%%' AND stdout='' AND payload_expired_at IS NOT NULL`)
		assertCount(t, pool, 5, `SELECT count(*) FROM ops.attempts WHERE id LIKE 'cancel-%%' AND stdout='keep' AND payload_expired_at IS NULL`)
	})
}

func testPool(t *testing.T, base, database, role string) *pgxpool.Pool {
	t.Helper()
	pc, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = database
	if role != "" {
		pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
			return err
		}
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func scalar[T any](t *testing.T, pool *pgxpool.Pool, query string) T {
	t.Helper()
	var value T
	if err := pool.QueryRow(context.Background(), query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertCount(t *testing.T, pool *pgxpool.Pool, want int, query string) {
	t.Helper()
	if got := scalar[int](t, pool, query); got != want {
		t.Fatalf("count = %d, want %d for %s", got, want, query)
	}
}
