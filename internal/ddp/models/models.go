// Package models applies the SQL relations declared in the registry.
package models

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

type Result struct {
	ModelRef string `json:"model_ref"`
	Status   string `json:"status"`
}

type outputColumn struct {
	oid  uint32
	name string
}

func Apply(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root string) ([]Result, error) {
	if pool == nil || cfg == nil {
		return nil, errors.New("models: pool and config are required")
	}
	order, err := orderModels(cfg)
	if err != nil {
		return nil, err
	}
	return applyRefs(ctx, pool, cfg, root, order, true)
}

// Refresh applies one registered model; its upstream relations must already exist.
func Refresh(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root, ref string) (Result, error) {
	if pool == nil || cfg == nil {
		return Result{}, errors.New("models: pool and config are required")
	}
	name, typed := strings.CutPrefix(ref, "model/")
	if _, exists := cfg.Models[name]; !typed || !exists {
		return Result{}, fmt.Errorf("unknown model %q", ref)
	}
	results, err := applyRefs(ctx, pool, cfg, root, []string{ref}, false)
	if err != nil {
		return Result{ModelRef: ref, Status: "failed"}, err
	}
	return results[0], nil
}

func applyRefs(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root string, order []string, checkData bool) ([]Result, error) {
	action := "models.refresh"
	if checkData {
		action = "models.apply"
	}
	results := make([]Result, 0, len(order))
	for _, ref := range order {
		model := cfg.Models[strings.TrimPrefix(ref, "model/")]
		sql, err := readSQL(root, model.File)
		if err != nil {
			return results, fmt.Errorf("%s: %w", ref, err)
		}
		id := ulid.Make().String()
		started := time.Now().UTC()
		err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO ops.model_refreshes (id,model_ref,started_at,status) VALUES ($1,$2,$3,'running')`, id, ref, started); err != nil {
				return pgError("record model refresh", err)
			}
			if err := applyRelation(ctx, tx, ref, model, sql); err != nil {
				return err
			}
			schema, name, err := relation(ref)
			if err != nil {
				return err
			}
			if err := verifyTx(ctx, tx, ref, model, checkData); err != nil {
				return err
			}
			if err := grantModelAccess(ctx, tx, schema, name); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE ops.model_refreshes SET finished_at=$1,status='succeeded' WHERE id=$2`, time.Now().UTC(), id)
			if err := pgError("record model success", err); err != nil {
				return err
			}
			return audit.Record(ctx, tx, action, ref, map[string]string{"status": "succeeded", "refresh_id": id})
		})
		if err != nil {
			if updateErr := recordFailure(ctx, pool, id, ref, started, err, action); updateErr != nil {
				return results, fmt.Errorf("%s: %w (record failure: %w)", ref, err, updateErr)
			}
			return results, fmt.Errorf("%s: %w", ref, err)
		}
		results = append(results, Result{ModelRef: ref, Status: "succeeded"})
	}
	return results, nil
}

func Verify(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config) error {
	if pool == nil || cfg == nil {
		return errors.New("models: pool and config are required")
	}
	order, err := orderModels(cfg)
	if err != nil {
		return err
	}
	for _, ref := range order {
		model := cfg.Models[strings.TrimPrefix(ref, "model/")]
		if model.Materialization != "view" && model.Materialization != "materialized_view" {
			return fmt.Errorf("%s: unsupported materialization %q", ref, model.Materialization)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		err = verifyTx(ctx, tx, ref, model, true)
		_ = tx.Rollback(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", ref, err)
		}
	}
	for name, table := range cfg.Tables {
		ref := "table/" + name
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		err = verifyRelation(ctx, tx, ref, table.Contract, true)
		_ = tx.Rollback(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", ref, err)
		}
	}
	return nil
}

func orderModels(cfg *config.Config) ([]string, error) {
	refs := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		refs = append(refs, "model/"+name)
	}
	sort.Strings(refs)
	state := map[string]uint8{}
	out := make([]string, 0, len(refs))
	var visit func(string) error
	visit = func(ref string) error {
		if state[ref] == 1 {
			return fmt.Errorf("model dependency cycle at %s", ref)
		}
		if state[ref] == 2 {
			return nil
		}
		state[ref] = 1
		m := cfg.Models[strings.TrimPrefix(ref, "model/")]
		for _, dep := range m.Reads {
			if strings.HasPrefix(dep, "model/") {
				if _, ok := cfg.Models[strings.TrimPrefix(dep, "model/")]; !ok {
					return fmt.Errorf("%s reads undeclared %s", ref, dep)
				}
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		state[ref] = 2
		out = append(out, ref)
		return nil
	}
	for _, ref := range refs {
		if err := visit(ref); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readSQL(root, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", errors.New("model file must be relative to project root")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	f, err := r.Open(filepath.ToSlash(name))
	if err != nil {
		return "", fmt.Errorf("open SQL: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", errors.New("model SQL is empty")
	}
	return string(b), nil
}

func relation(ref string) (string, string, error) {
	n := strings.TrimPrefix(ref, "model/")
	s, name, ok := strings.Cut(n, ".")
	if !ok || s == "" || name == "" || strings.Contains(name, ".") {
		return "", "", fmt.Errorf("invalid model reference %q", ref)
	}
	return s, name, nil
}
func safeError(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return "postgres " + pe.Code
	}
	return err.Error()
}
func pgError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, audit.ErrRefused) {
		return fmt.Errorf("%s: %w", op, audit.ErrRefused)
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42501" {
		return fmt.Errorf("%s: %w", op, audit.ErrRefused)
	}
	return fmt.Errorf("%s: %s", op, safeError(err))
}

func recordFailure(parent context.Context, pool *pgxpool.Pool, id, ref string, started time.Time, failure error, action string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ops.model_refreshes (id,model_ref,started_at,finished_at,status,error) VALUES ($1,$2,$3,$4,'failed',$5)`, id, ref, started, time.Now().UTC(), safeError(failure)); err != nil {
			return pgError("record model failure", err)
		}
		return audit.Record(ctx, tx, action, ref, map[string]string{"status": "failed", "refresh_id": id})
	})
}

func verifyTx(ctx context.Context, tx pgx.Tx, ref string, model config.Model, checkData bool) error {
	kind, _, err := currentRelation(ctx, tx, ref)
	if err != nil {
		return err
	}
	if kind != model.Materialization {
		return fmt.Errorf("materialization mismatch: got %q, want %q", kind, model.Materialization)
	}
	if err := verifyDependencies(ctx, tx, ref, model.Reads); err != nil {
		return err
	}
	return verifyRelation(ctx, tx, ref, model.Contract, checkData)
}

func grantModelAccess(ctx context.Context, tx pgx.Tx, schema, name string) error {
	roles := []string{"ddp_job", "ddp_readonly"}
	if schema == "core" || schema == "mart" {
		roles = append(roles, "ddp_api")
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx, "GRANT SELECT ON "+pgx.Identifier{schema, name}.Sanitize()+" TO "+pgx.Identifier{role}.Sanitize()); err != nil {
			return pgError("grant model access", err)
		}
	}
	return nil
}

func verifyDependencies(ctx context.Context, tx pgx.Tx, ref string, declared []string) error {
	schema, name, err := relation(ref)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT n.nspname, c.relname, c.relkind
		FROM pg_rewrite r
		JOIN pg_depend d ON d.classid = 'pg_rewrite'::regclass AND d.objid = r.oid
		JOIN pg_class c ON c.oid = d.refobjid AND d.refclassid = 'pg_class'::regclass
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE r.ev_class = to_regclass($1)::oid
		  AND r.rulename = '_RETURN'
		  AND c.oid <> r.ev_class
		  AND c.relkind IN ('r', 'p', 'f', 'v', 'm')`, schema+"."+name)
	if err != nil {
		return pgError("inspect dependencies", err)
	}
	defer rows.Close()
	actual := map[string]string{}
	for rows.Next() {
		var depSchema, depName, kind string
		if err := rows.Scan(&depSchema, &depName, &kind); err != nil {
			return pgError("inspect dependencies", err)
		}
		actual[depSchema+"."+depName] = kind
	}
	if err := rows.Err(); err != nil {
		return pgError("inspect dependencies", err)
	}

	declaredRelations := map[string]string{}
	for _, dep := range declared {
		depSchema, depName, err := relation(strings.Replace(dep, "table/", "model/", 1))
		if err != nil {
			return err
		}
		depRelation := depSchema + "." + depName
		if previous, ok := declaredRelations[depRelation]; ok && previous != dep {
			return fmt.Errorf("dependency %s is declared as both %s and %s", depRelation, previous, dep)
		}
		declaredRelations[depRelation] = dep
	}
	for relation, kind := range actual {
		decl, ok := declaredRelations[relation]
		if !ok {
			kind := "table"
			if actual[relation] == "v" || actual[relation] == "m" {
				kind = "model"
			}
			return fmt.Errorf("undeclared direct dependency %s/%s", kind, relation)
		}
		expected := "table/" + relation
		if kind == "v" || kind == "m" {
			expected = "model/" + relation
		}
		if decl != expected {
			return fmt.Errorf("direct dependency %s has PostgreSQL kind %s; declare %s", decl, kind, expected)
		}
	}
	for relation, decl := range declaredRelations {
		if _, ok := actual[relation]; !ok {
			return fmt.Errorf("declared dependency %s is not a direct PostgreSQL dependency", decl)
		}
	}
	return nil
}

func verifyRelation(ctx context.Context, tx pgx.Tx, ref string, contract config.Contract, checkData bool) error {
	schema, name, err := relation(strings.Replace(ref, "table/", "model/", 1))
	if err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+"."+name).Scan(&exists); err != nil {
		return pgError("inspect relation", err)
	}
	if !exists {
		return errors.New("relation was not created")
	}
	rows, err := tx.Query(ctx, `SELECT a.attname, a.atttypid::oid, format_type(a.atttypid,a.atttypmod) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, schema, name)
	if err != nil {
		return pgError("inspect output columns", err)
	}
	defer rows.Close()
	actual := map[string]outputColumn{}
	for rows.Next() {
		var col, typ string
		var oid uint32
		if err := rows.Scan(&col, &oid, &typ); err != nil {
			return pgError("inspect output columns", err)
		}
		actual[col] = outputColumn{oid: oid, name: typ}
	}
	if err := rows.Err(); err != nil {
		return pgError("inspect output columns", err)
	}
	for col, want := range contract.Columns {
		got, ok := actual[col]
		if !ok {
			return fmt.Errorf("missing output column %q", col)
		}
		var compatible bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE($1::oid = to_regtype($2)::oid OR EXISTS (SELECT FROM pg_cast WHERE castsource=$1::oid AND casttarget=to_regtype($2)::oid AND castcontext='i'), false)`, got.oid, want.Type).Scan(&compatible); err != nil {
			return pgError("verify output type", err)
		}
		if !compatible {
			return fmt.Errorf("column %q type: got %s, want %s", col, got.name, want.Type)
		}
		if !checkData {
			continue
		}
		q := "SELECT EXISTS (SELECT 1 FROM " + pgx.Identifier{schema, name}.Sanitize() + " WHERE " + pgx.Identifier{col}.Sanitize() + " IS NULL)"
		var hasNull bool
		if err := tx.QueryRow(ctx, q).Scan(&hasNull); err != nil {
			return pgError("verify output nullability", err)
		}
		if !want.Nullable && hasNull {
			return fmt.Errorf("column %q contains NULL", col)
		}
	}
	if !checkData {
		for _, key := range append(append([]string{}, contract.PrimaryKey...), contract.UniqueKey...) {
			if _, ok := actual[key]; !ok {
				return fmt.Errorf("key column %q missing", key)
			}
		}
		return nil
	}
	if err := verifyKey(ctx, tx, schema, name, "primary key", contract.PrimaryKey, actual); err != nil {
		return err
	}
	return verifyKey(ctx, tx, schema, name, "unique key", contract.UniqueKey, actual)
}

func verifyKey(ctx context.Context, tx pgx.Tx, schema, name, kind string, key []string, actual map[string]outputColumn) error {
	if len(key) == 0 {
		return nil
	}
	cols := make([]string, len(key))
	for i, col := range key {
		if _, ok := actual[col]; !ok {
			return fmt.Errorf("%s column %q missing", kind, col)
		}
		cols[i] = pgx.Identifier{col}.Sanitize()
	}
	relation := pgx.Identifier{schema, name}.Sanitize()
	q := "SELECT EXISTS (SELECT 1 FROM " + relation + " WHERE " + strings.Join(cols, " IS NULL OR ") + " IS NULL)"
	var invalid bool
	if err := tx.QueryRow(ctx, q).Scan(&invalid); err != nil {
		return pgError("verify "+kind+" nullability", err)
	}
	if invalid {
		return fmt.Errorf("%s contains NULL values", kind)
	}
	q = "SELECT EXISTS (SELECT 1 FROM " + relation + " GROUP BY " + strings.Join(cols, ",") + " HAVING COUNT(*) > 1)"
	if err := tx.QueryRow(ctx, q).Scan(&invalid); err != nil {
		return pgError("verify "+kind+" uniqueness", err)
	}
	if invalid {
		return fmt.Errorf("%s contains duplicate values", kind)
	}
	return nil
}
