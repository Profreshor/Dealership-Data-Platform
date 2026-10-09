package models

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestApplyReadonlyPermissionIsRefused(t *testing.T) {
	owner := testPool(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `CREATE TABLE app.readonly_source (id integer)`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeSQL(t, root, "model.sql", `SELECT id FROM app.readonly_source`)
	cfg := &config.Config{Models: map[string]config.Model{
		"mart.readonly_model": {File: "model.sql", Materialization: "view", Reads: []string{"table/app.readonly_source"}, Contract: contract(map[string]config.Column{"id": {Type: "integer"}}, nil, nil)},
	}}

	var database string
	if err := owner.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	base, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	base.ConnConfig.Database = database
	base.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE ddp_readonly`)
		return err
	}
	readonly, err := pgxpool.NewWithConfig(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()

	_, err = Apply(ctx, readonly, cfg, root)
	if !errors.Is(err, audit.ErrRefused) {
		t.Fatalf("readonly apply error=%v, want audit.ErrRefused", err)
	}
}
