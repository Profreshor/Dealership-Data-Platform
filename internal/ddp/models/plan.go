package models

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PlanStep struct {
	ModelRef               string   `json:"model_ref"`
	File                   string   `json:"file"`
	Materialization        string   `json:"materialization"`
	Reads                  []string `json:"reads"`
	CurrentMaterialization string   `json:"current_materialization"`
	CurrentSQL             *string  `json:"current_sql"`
	ProposedSQL            string   `json:"proposed_sql"`
}

// Plan inspects the registered models and database catalog without changing either.
func Plan(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root string) ([]PlanStep, error) {
	if pool == nil || cfg == nil {
		return nil, errors.New("models: pool and config are required")
	}
	order, err := orderModels(cfg)
	if err != nil {
		return nil, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, pgError("begin model plan", err)
	}
	defer tx.Rollback(ctx)

	steps := make([]PlanStep, 0, len(order))
	for _, ref := range order {
		model := cfg.Models[strings.TrimPrefix(ref, "model/")]
		proposed, err := readSQL(root, model.File)
		if err != nil {
			return steps, fmt.Errorf("%s: %w", ref, err)
		}
		currentMaterialization, currentSQL, err := currentRelation(ctx, tx, ref)
		if err != nil {
			return steps, fmt.Errorf("%s: %w", ref, err)
		}
		steps = append(steps, PlanStep{
			ModelRef:               ref,
			File:                   model.File,
			Materialization:        model.Materialization,
			Reads:                  append([]string{}, model.Reads...),
			CurrentMaterialization: currentMaterialization,
			CurrentSQL:             currentSQL,
			ProposedSQL:            proposed,
		})
	}
	return steps, nil
}
