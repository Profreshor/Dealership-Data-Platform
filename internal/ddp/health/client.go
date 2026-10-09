package health

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

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxSQLSize = 256 << 10

// Client evaluates all declared client rules in name order.
func Client(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root string, now time.Time) []Observation {
	if cfg == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Health))
	for name := range cfg.Health {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Observation, 0, len(names))
	for _, name := range names {
		ruleCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		h := cfg.Health[name]
		o := Observation{Ref: "health/" + name, Target: h.Target, Severity: h.Severity, Notify: append([]string(nil), h.Notify...)}
		if pool == nil {
			o.State, o.Message = "unknown", "health check unavailable"
		} else if h.Kind == "freshness" {
			evaluateFreshness(ruleCtx, pool, h, now, &o)
		} else {
			evaluateSQL(ruleCtx, pool, h, root, &o)
		}
		cancel()
		out = append(out, o)
	}
	return out
}

func evaluateFreshness(ctx context.Context, pool *pgxpool.Pool, h config.Health, now time.Time, o *Observation) {
	d, err := time.ParseDuration(h.MaxAge)
	if err != nil || d <= 0 {
		o.State, o.Message = "unknown", "invalid freshness configuration"
		return
	}
	_, rel, _ := strings.Cut(h.Target, "/")
	parts := strings.Split(rel, ".")
	if len(parts) != 2 {
		o.State, o.Message = "unknown", "invalid freshness target"
		return
	}
	query := fmt.Sprintf("SELECT max(%s) FROM %s", pgx.Identifier{h.Column}.Sanitize(), pgx.Identifier{parts[0], parts[1]}.Sanitize())
	var latest *time.Time
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		o.State, o.Message = "unknown", "health check unavailable"
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
		o.State, o.Message = "unknown", "health check unavailable"
		return
	}
	if err = tx.QueryRow(ctx, query).Scan(&latest); err != nil {
		o.State, o.Message = "unknown", "health check unavailable"
		return
	}
	if latest == nil {
		o.State, o.Message = "failing", "no data"
		return
	}
	if latest.After(now) {
		o.State, o.Message = "unknown", "future timestamp"
		return
	}
	o.Value = latest.UTC().Format(time.RFC3339Nano)
	if now.Sub(*latest) > d {
		o.State, o.Message = "failing", "data is stale"
	} else {
		o.State, o.Message = "ok", "fresh"
	}
}

func evaluateSQL(ctx context.Context, pool *pgxpool.Pool, h config.Health, root string, o *Observation) {
	sql, err := readHealthSQL(root, h.SQL)
	sql = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	if err != nil || sql == "" {
		o.State, o.Message = "unknown", "invalid health query"
		return
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		o.State, o.Message = "unknown", "health check unavailable"
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
		o.State, o.Message = "unknown", "health check unavailable"
		return
	}
	query := "SELECT * FROM (\n" + sql + "\n) AS health_source LIMIT 2"
	description, err := tx.Conn().Prepare(ctx, "", query)
	if err != nil {
		o.State, o.Message = "unknown", "health check unavailable"
		return
	}
	fields := description.Fields
	if len(fields) < 1 || len(fields) > 3 {
		o.State, o.Message = "unknown", "invalid health result"
		return
	}
	okIndex := -1
	seen := map[string]bool{}
	for i := range fields {
		n := strings.ToLower(string(fields[i].Name))
		if seen[n] || (n != "ok" && n != "message" && n != "value") {
			o.State, o.Message = "unknown", "invalid health result"
			return
		}
		seen[n] = true
		if n == "ok" {
			okIndex = i
			if fields[i].DataTypeOID != 16 {
				o.State, o.Message = "unknown", "invalid health result"
				return
			}
		} else if fields[i].DataTypeOID != 25 {
			o.State, o.Message = "unknown", "invalid health result"
			return
		}
	}
	if okIndex < 0 {
		o.State, o.Message = "unknown", "invalid health result"
		return
	}
	// Describe before execution and bound text on the server, before pgx receives it.
	// MATERIALIZED evaluates volatile expressions once and retains at most two rows.
	columns := make([]string, len(fields))
	for i, field := range fields {
		name := pgx.Identifier{field.Name}.Sanitize()
		columns[i] = name
		if i != okIndex {
			limit := 2048
			if strings.EqualFold(field.Name, "value") {
				limit = 512
			}
			columns[i] = fmt.Sprintf("CASE WHEN octet_length(%s)>%d THEN repeat('!',%d) ELSE %s END AS %s", name, limit, limit+1, name, name)
		}
	}
	rows, err := tx.Query(ctx, "WITH health_check AS MATERIALIZED ("+query+") SELECT "+strings.Join(columns, ",")+" FROM health_check")
	if err != nil {
		o.State, o.Message = "unknown", "health check unavailable"
		return
	}
	defer rows.Close()
	if !rows.Next() {
		o.State, o.Message = "unknown", "empty health result"
		return
	}
	values, err := rows.Values()
	if err != nil || len(values) != len(fields) {
		o.State, o.Message = "unknown", "invalid health result"
		return
	}
	ok, valid := values[okIndex].(bool)
	if !valid {
		o.State, o.Message = "unknown", "invalid health result"
		return
	}
	for i := range values {
		if i == okIndex || values[i] == nil {
			continue
		}
		s, valid := values[i].(string)
		if !valid || len(s) > map[string]int{"message": 2048, "value": 512}[strings.ToLower(string(fields[i].Name))] {
			o.State, o.Message = "unknown", "invalid health result"
			return
		}
		if strings.EqualFold(string(fields[i].Name), "message") {
			o.Message = s
		} else {
			o.Value = s
		}
	}
	if rows.Next() {
		o.State, o.Message = "unknown", "invalid health result"
		return
	}
	if err := rows.Err(); err != nil {
		o.State, o.Message = "unknown", "health check unavailable"
		return
	}
	if o.Message == "" {
		if ok {
			o.Message = "check passed"
		} else {
			o.Message = "check failed"
		}
	}
	if ok {
		o.State = "ok"
	} else {
		o.State = "failing"
	}
}

func readHealthSQL(root, name string) (string, error) {
	if root == "" || !filepath.IsLocal(name) {
		return "", errors.New("invalid path")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	info, err := dir.Stat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSQLSize {
		return "", errors.New("invalid sql file")
	}
	f, err := dir.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSQLSize+1))
	if err != nil || len(b) > maxSQLSize {
		return "", errors.New("invalid sql file")
	}
	return string(b), nil
}
