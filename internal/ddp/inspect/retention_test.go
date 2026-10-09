package inspect

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestExpiredAttemptPayloadIsMarkedAndMasked(t *testing.T) {
	owner, cfg := diagnosticDB(t)
	id := ulid.Make().String()
	if _, err := owner.Exec(t.Context(), `INSERT INTO ops.executions(id,job_ref,status,scheduled_at,finished_at) VALUES($1,'job/sync_customers','failed',clock_timestamp(),clock_timestamp())`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(t.Context(), `INSERT INTO ops.attempts(id,execution_id,number,status,finished_at,stdout,stderr,error,payload_expired_at) VALUES($1,$2,1,'failed',clock_timestamp(),'secret out','secret err','secret failure',clock_timestamp())`, ulid.Make().String(), id); err != nil {
		t.Fatal(err)
	}
	readerCfg := owner.Config().Copy()
	readerCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	reader, err := pgxpool.NewWithConfig(t.Context(), readerCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	logs, err := Logs(t.Context(), reader, "job/sync_customers", 10)
	if err != nil || len(logs) != 1 || logs[0].Stdout != "" || logs[0].Stderr != "" || logs[0].Error != nil || logs[0].PayloadExpiredAt == nil {
		t.Fatalf("masked logs: %+v %v", logs, err)
	}
	diagnosis, err := Diagnose(t.Context(), reader, cfg, "job/sync_customers")
	if err != nil || len(diagnosis.Failures) != 1 || diagnosis.Failures[0].LastAttempt == nil || diagnosis.Failures[0].LastAttempt.PayloadExpiredAt == nil || diagnosis.Failures[0].LastAttempt.Stdout != "" {
		t.Fatalf("masked diagnosis: %+v %v", diagnosis, err)
	}
}
