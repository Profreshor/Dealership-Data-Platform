package inspect

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func TestDiagnosisTotalFailuresCountsOnlyLatestFailedRuns(t *testing.T) {
	owner, _ := diagnosticDB(t)
	cfg, err := config.Load("../testdata/reporting/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	base := cfg.Jobs["sync_customers"]
	args := make([]any, 0, 50)
	values := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		name := "sync_customers_verification_" + string(rune('a'+i))
		cfg.Jobs[name] = base
		values = append(values, "($"+strconv.Itoa(i+1)+",'job/"+name+"','failed',clock_timestamp(),clock_timestamp())")
		args = append(args, ulid.Make().String())
	}
	if _, err := owner.Exec(ctx, "INSERT INTO ops.executions(id,job_ref,status,scheduled_at,finished_at) VALUES "+strings.Join(values, ","), args...); err != nil {
		t.Fatal(err)
	}

	readCfg := owner.Config().Copy()
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

	diagnosis, err := Diagnose(ctx, reader, cfg, "page/customers")
	if err != nil {
		t.Fatal(err)
	}
	if diagnosis.TotalFailures != 25 || len(diagnosis.Failures) != 20 {
		t.Fatalf("total_failures=%d displayed=%d, want 25 and 20", diagnosis.TotalFailures, len(diagnosis.Failures))
	}
}
