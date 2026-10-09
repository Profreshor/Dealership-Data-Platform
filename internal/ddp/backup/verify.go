package backup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/endpoints"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// verifyRestore runs the restore read-only smoke against a restored database.
func verifyRestore(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) error {
	if pool == nil || cfg == nil {
		return errors.New("restore read-only smoke: pool and config are required")
	}
	readOnlyCfg := pool.Config().Copy()
	// Contract checks need the maintenance connection's visibility of landed
	// relations, which the API may deliberately have no permission to read.
	delete(readOnlyCfg.ConnConfig.RuntimeParams, "role")
	readOnlyCfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	readOnlyPool, err := pgxpool.NewWithConfig(ctx, readOnlyCfg)
	if err != nil {
		return fmt.Errorf("restore read-only smoke: open API pool: %w", err)
	}
	defer readOnlyPool.Close()
	conn, err := readOnlyPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("restore read-only smoke: acquire connection: %w", err)
	}
	entries, err := migrate.Status(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		return fmt.Errorf("restore read-only smoke: migration status: %w", err)
	}
	for _, entry := range entries {
		if !entry.Applied {
			return fmt.Errorf("restore read-only smoke: migration %s/%s is not applied", entry.Kind, entry.ID)
		}
	}
	if err := models.Verify(ctx, readOnlyPool, cfg); err != nil {
		return fmt.Errorf("restore read-only smoke: contracts: %w", err)
	}
	apiCfg := readOnlyCfg.Copy()
	apiCfg.ConnConfig.RuntimeParams["role"] = "ddp_api"
	apiPool, err := pgxpool.NewWithConfig(ctx, apiCfg)
	if err != nil {
		return fmt.Errorf("restore read-only smoke: open API pool: %w", err)
	}
	defer apiPool.Close()
	for name, endpoint := range cfg.Endpoints {
		params := url.Values{}
		if endpoint.Shape != "singleton" {
			params.Set("limit", "1")
		}
		query, err := endpoints.Plan("endpoint/"+name, endpoint, params, "list")
		if err != nil {
			return fmt.Errorf("restore read-only smoke: endpoint/%s plan: %w", name, err)
		}
		if err := verifyEndpointQuery(ctx, apiPool, query, endpoint.Shape == "singleton"); err != nil {
			return fmt.Errorf("restore read-only smoke: endpoint/%s query: %w", name, err)
		}
	}
	return nil
}

func verifyEndpointQuery(ctx context.Context, pool *pgxpool.Pool, query endpoints.Query, singleton bool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	workCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(workCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := tx.Exec(workCtx, "SELECT set_config('statement_timeout', '5000', true), set_config('lock_timeout', '5000', true)"); err != nil {
		return err
	}
	rows, err := tx.Query(workCtx, query.SQL, query.Args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if singleton && count > 1 {
			return errors.New("endpoint returned more than one row")
		}
	}
	return rows.Err()
}
