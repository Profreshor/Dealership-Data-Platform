package models

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

func applyRelation(ctx context.Context, tx pgx.Tx, ref string, model config.Model, statement string) error {
	if model.Materialization != "view" && model.Materialization != "materialized_view" {
		return fmt.Errorf("unsupported materialization %q", model.Materialization)
	}
	if model.Materialization == "materialized_view" && len(model.Contract.UniqueKey) == 0 {
		return errors.New("materialized view requires unique_key")
	}
	schema, name, err := relation(ref)
	if err != nil {
		return err
	}
	target := pgx.Identifier{schema, name}.Sanitize()
	kind, definition, err := currentRelation(ctx, tx, ref)
	if err != nil {
		return err
	}
	if kind != "" && kind != "view" && kind != "materialized_view" {
		return fmt.Errorf("model target already exists as %s", kind)
	}
	if kind == "materialized_view" && model.Materialization == kind {
		// Let Postgres normalize the candidate SQL without executing the SELECT.
		// The comparison view is dropped in this transaction and never becomes visible.
		candidateName := "_ddp_candidate_" + strings.ToLower(ulid.Make().String())
		candidate := pgx.Identifier{schema, candidateName}.Sanitize()
		if err := selectDDL(ctx, tx, "CREATE VIEW "+candidate+" AS "+statement); err != nil {
			return err
		}
		_, proposed, err := currentRelation(ctx, tx, "model/"+schema+"."+candidateName)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DROP VIEW "+candidate); err != nil {
			return pgError("drop comparison view", err)
		}
		if definition != nil && proposed != nil && *definition == *proposed {
			// ponytail: ordinary refresh locks readers; add CONCURRENTLY and a unique
			// index if measured refresh latency requires reads during refresh.
			_, err := tx.Exec(ctx, "REFRESH MATERIALIZED VIEW "+target)
			return pgError("refresh materialized view", err)
		}
	}
	if kind != "" && (kind != "view" || model.Materialization != "view") {
		command := "DROP VIEW "
		if kind == "materialized_view" {
			command = "DROP MATERIALIZED VIEW "
		}
		if _, err := tx.Exec(ctx, command+target+" RESTRICT"); err != nil {
			return pgError("replace model (dependent relations must be rebuilt together)", err)
		}
	}
	command := "CREATE OR REPLACE VIEW "
	if model.Materialization == "materialized_view" {
		command = "CREATE MATERIALIZED VIEW "
	}
	return selectDDL(ctx, tx, command+target+" AS "+statement)
}

func selectDDL(ctx context.Context, tx pgx.Tx, statement string) error {
	result := tx.Conn().PgConn().ExecParams(ctx, statement, nil, nil, nil, nil).Read()
	return pgError("apply model SQL", result.Err)
}

// currentRelation reads catalog facts only; it does not query model rows.
func currentRelation(ctx context.Context, tx pgx.Tx, ref string) (string, *string, error) {
	schema, name, err := relation(ref)
	if err != nil {
		return "", nil, err
	}
	var kind string
	var definition *string
	err = tx.QueryRow(ctx, `SELECT CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized_view' WHEN 'r' THEN 'table' WHEN 'p' THEN 'table' ELSE 'other relation' END,
		CASE WHEN c.relkind IN ('v','m') THEN pg_get_viewdef(c.oid, false) END
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2`, schema, name).Scan(&kind, &definition)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	return kind, definition, pgError("inspect model", err)
}
