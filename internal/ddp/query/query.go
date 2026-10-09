// Package query executes one bounded SQL statement on a caller-owned connection.
package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrInvalid = errors.New("invalid SQL query")
var ErrLimit = errors.New("query result limit exceeded")

type Options struct {
	Write   bool
	Limit   int
	Timeout time.Duration
}
type Column struct {
	Name    string `json:"name"`
	TypeOID uint32 `json:"type_oid"`
}
type Result struct {
	Columns      []Column    `json:"columns"`
	Rows         [][]*string `json:"rows"`
	Command      string      `json:"command"`
	RowsAffected int64       `json:"rows_affected"`
}

const maxStatement = 256 * 1024
const maxBytes = 4 * 1024 * 1024

func Fingerprint(statement string) string {
	sum := sha256.Sum256([]byte(statement))
	return hex.EncodeToString(sum[:])
}

// Run requires a dedicated connection, which the caller must close after use.
func Run(ctx context.Context, conn *pgx.Conn, statement string, options Options) (result Result, runErr error) {
	if err := validate(statement, options); err != nil {
		return Result{}, err
	}
	if err := guard(statement, options.Write); err != nil {
		return Result{}, err
	}
	workCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	mode := pgx.ReadOnly
	if options.Write {
		mode = pgx.ReadWrite
	}
	tx, err := conn.BeginTx(workCtx, pgx.TxOptions{AccessMode: mode})
	if err != nil {
		return Result{}, sanitize(err)
	}
	attempted, committed := false, false
	failureStatus := "failed"
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		if err := tx.Rollback(cleanupCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			runErr = errors.Join(runErr, errors.New("SQL rollback could not be confirmed"))
		}
		if options.Write && attempted {
			runErr = errors.Join(runErr, recordFailure(ctx, conn, statement, failureStatus, runErr))
		}
	}()
	principal, err := sessionPrincipal(workCtx, tx)
	if err != nil {
		return Result{}, sanitize(err)
	}
	milliseconds := max(1, options.Timeout.Milliseconds())
	if _, err = tx.Exec(workCtx, "select set_config('statement_timeout', $1, true), set_config('lock_timeout', $1, true)", fmt.Sprint(milliseconds)); err != nil {
		return Result{}, sanitize(err)
	}
	attempted = true
	result, err = execute(workCtx, cancel, tx, statement, options.Limit)
	if err != nil {
		return Result{}, sanitize(err)
	}
	if conn.PgConn().TxStatus() != 'T' {
		return Result{}, errors.New("SQL transaction ended unexpectedly")
	}
	after, err := sessionPrincipal(workCtx, tx)
	if err != nil {
		return Result{}, sanitize(err)
	}
	if after != principal {
		return Result{}, audit.ErrRefused
	}
	if options.Write {
		if err := audit.Record(workCtx, tx, "sql.write", "sql/"+Fingerprint(statement), map[string]any{"status": "succeeded", "command": result.Command, "rows_affected": result.RowsAffected}); err != nil {
			return Result{}, sanitize(err)
		}
	}
	if err := tx.Commit(workCtx); err != nil {
		// A lost acknowledgement does not prove that COMMIT failed.
		failureStatus = "unknown"
		return Result{}, fmt.Errorf("SQL commit outcome unknown: %w", sanitize(err))
	}
	committed = true
	return result, nil
}

func validate(statement string, options Options) error {
	if strings.TrimSpace(statement) == "" || len(statement) > maxStatement || strings.IndexByte(statement, 0) >= 0 {
		return ErrInvalid
	}
	if options.Limit < 1 || options.Limit > 10000 || options.Timeout <= 0 || options.Timeout > 5*time.Minute {
		return ErrInvalid
	}
	return nil
}
func sessionPrincipal(ctx context.Context, tx pgx.Tx) (string, error) {
	var session, current string
	if err := tx.QueryRow(ctx, "select session_user, current_user").Scan(&session, &current); err != nil {
		return "", err
	}
	return session + "\x00" + current, nil
}

func execute(ctx context.Context, cancel context.CancelFunc, tx pgx.Tx, statement string, limit int) (Result, error) {
	rows, err := tx.Query(ctx, statement, pgx.QueryExecModeExec, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	result := Result{Columns: make([]Column, len(fields)), Rows: make([][]*string, 0, min(limit, 32))}
	for i, field := range fields {
		result.Columns[i] = Column{Name: field.Name, TypeOID: field.DataTypeOID}
	}
	bytesUsed := 0
	for rows.Next() {
		if len(result.Rows) >= limit {
			cancel()
			rows.Close()
			return Result{}, ErrLimit
		}
		values := rows.RawValues()
		row := make([]*string, len(values))
		for i, value := range values {
			if value == nil {
				continue
			}
			// ponytail: pgx buffers a wire row; use COPY streaming if large cells become necessary.
			bytesUsed += len(value)
			if bytesUsed > maxBytes {
				cancel()
				return Result{}, ErrLimit
			}
			text := string(value)
			row[i] = &text
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Result{}, err
	}
	rows.Close()
	tag := rows.CommandTag()
	result.Command, result.RowsAffected = tag.String(), tag.RowsAffected()
	return result, nil
}

func recordFailure(ctx context.Context, conn *pgx.Conn, statement, status string, cause error) error {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(auditCtx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return errors.New("SQL failure audit could not be recorded")
	}
	defer func() { _ = tx.Rollback(auditCtx) }()
	outcome := map[string]any{"status": status, "error": sanitize(cause).Error()}
	if err := audit.Record(auditCtx, tx, "sql.write", "sql/"+Fingerprint(statement), outcome); err != nil {
		return errors.New("SQL failure audit could not be recorded")
	}
	if err := tx.Commit(auditCtx); err != nil {
		return errors.New("SQL failure audit could not be confirmed")
	}
	return nil
}

func sanitize(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "42501" || pgErr.Code == "25006" {
			return fmt.Errorf("%w: SQLSTATE %s", audit.ErrRefused, pgErr.Code)
		}
		return fmt.Errorf("SQLSTATE %s", pgErr.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("query timed out")
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	for _, known := range []error{ErrInvalid, ErrLimit, audit.ErrRefused} {
		if errors.Is(err, known) {
			return known
		}
	}
	return errors.New("SQL execution failed")
}

func guard(statement string, write bool) error {
	keyword := firstKeyword(statement)
	if keyword == "" {
		return ErrInvalid
	}
	forbidden := map[string]bool{"BEGIN": true, "START": true, "COMMIT": true, "END": true, "ROLLBACK": true, "ABORT": true, "SAVEPOINT": true, "RELEASE": true, "PREPARE": true, "EXECUTE": true, "DEALLOCATE": true, "SET": true, "RESET": true, "DISCARD": true, "COPY": true}
	if forbidden[keyword] {
		return fmt.Errorf("%w: transaction/session control is not allowed", ErrInvalid)
	}
	if !write {
		allowed := map[string]bool{"SELECT": true, "WITH": true, "VALUES": true, "SHOW": true, "EXPLAIN": true}
		if !allowed[keyword] {
			return audit.ErrRefused
		}
	}
	return nil
}

func firstKeyword(sql string) string {
	depth := 0
	for i := 0; i < len(sql); {
		if strings.ContainsRune(" \t\r\n\f\v", rune(sql[i])) {
			i++
			continue
		}
		if sql[i] == '(' {
			depth++
			i++
			continue
		}
		if depth > 0 && sql[i] == ')' {
			depth--
			i++
			continue
		}
		if sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			i += 2
			for i < len(sql) && sql[i] != '\n' && sql[i] != '\r' {
				i++
			}
			continue
		}
		if sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			i += 2
			nested := 1
			for i < len(sql) && nested > 0 {
				if i+1 < len(sql) && sql[i] == '/' && sql[i+1] == '*' {
					nested++
					i += 2
				} else if i+1 < len(sql) && sql[i] == '*' && sql[i+1] == '/' {
					nested--
					i += 2
				} else {
					i++
				}
			}
			continue
		}
		start := i
		for i < len(sql) && (sql[i] >= 'a' && sql[i] <= 'z' || sql[i] >= 'A' && sql[i] <= 'Z' || sql[i] == '_') {
			i++
		}
		if start == i {
			return ""
		}
		return strings.ToUpper(sql[start:i])
	}
	return ""
}
