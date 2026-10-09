// Package audit records privileged operations in the transaction that changes state.
package audit

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"
)

var ErrRefused = errors.New("operation refused")

// Record uses the authenticated database session as principal. Callers supply
// bounded operational facts, never connection strings or workload data.
func Record(ctx context.Context, tx pgx.Tx, action, target string, outcome any) error {
	data, err := json.Marshal(outcome)
	if err != nil {
		return errors.New("encode operation audit")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ddp.audit(id,action,target,outcome) VALUES($1,$2,$3,$4)`, ulid.Make().String(), action, target, string(data)); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "42501" {
			return ErrRefused
		}
		return errors.New("record operation audit")
	}
	return nil
}
