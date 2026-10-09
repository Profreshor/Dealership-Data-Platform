package inspect

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestExpiredAttemptKeepsLatestFailureStatusAndMarker(t *testing.T) {
	owner, cfg := diagnosticDB(t)
	ctx := t.Context()
	executionID := ulid.Make().String()
	if _, err := owner.Exec(ctx, `INSERT INTO ops.executions(id,job_ref,status,scheduled_at,finished_at) VALUES($1,'job/sync_customers','failed',clock_timestamp(),clock_timestamp())`, executionID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO ops.attempts(id,execution_id,number,status,finished_at,stdout,stderr,error,payload_expired_at) VALUES($1,$2,7,'failed',clock_timestamp(),'secret out','secret err','secret failure',clock_timestamp())`, ulid.Make().String(), executionID); err != nil {
		t.Fatal(err)
	}
	readerCfg := owner.Config().Copy()
	readerCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE ddp_readonly")
		return err
	}
	reader, err := pgxpool.NewWithConfig(ctx, readerCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	logs, err := Logs(ctx, reader, "execution/"+executionID, 10)
	if err != nil || len(logs) != 1 || logs[0].Status != "failed" || logs[0].Number != 7 || logs[0].PayloadExpiredAt == nil || logs[0].Stdout != "" || logs[0].Stderr != "" || logs[0].Error != nil {
		t.Fatalf("expired logs: %+v %v", logs, err)
	}
	diagnosis, err := Diagnose(ctx, reader, cfg, "execution/"+executionID)
	if err != nil || diagnosis.Execution == nil || diagnosis.Execution.Status != "failed" || diagnosis.Execution.LastAttempt == nil || diagnosis.Execution.LastAttempt.Status != "failed" || diagnosis.Execution.LastAttempt.PayloadExpiredAt == nil || diagnosis.Execution.LastAttempt.Stdout != "" || diagnosis.Execution.LastAttempt.Error != nil {
		t.Fatalf("expired diagnosis: %+v %v", diagnosis, err)
	}
}
