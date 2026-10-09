package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/query"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
)

func sqlCommand(catalog commandCatalog, write func(any) error, oneRef cobra.PositionalArgs) *cobra.Command {
	var writeMode bool
	var confirm string
	var limit int
	var timeout time.Duration
	command := catalog.declare(commandPolicy{"write", "administrator", "read-only by default; --write requires --confirm matching SQL SHA-256", "transactional: sql.write; failed attempts after rollback"}, &cobra.Command{
		Use: "sql <query>", Short: "Run a bounded SQL query", Long: "Run one statement using DATABASE_URL, with a read-only transaction by default. Readers need only their database grants. --write requires --confirm matching the exact SQL SHA-256 and permission to write both the target and its audit. Transaction/session control and COPY are refused. Rows are positional PostgreSQL text values with JSON null for SQL NULL. Exceeding limits returns an error without partial results. No registry or environment secrets are resolved.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := oneRef(cmd, args); err != nil {
				return err
			}
			if err := validateSQLArgs(args[0], writeMode, confirm, limit, timeout); err != nil {
				return err
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			url := os.Getenv("DATABASE_URL")
			if url == "" {
				return errors.New("DATABASE_URL is required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			conn, err := pgx.Connect(ctx, url)
			if err != nil {
				return fmt.Errorf("connect to database: %s", safeDBError(err))
			}
			defer func() {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer closeCancel()
				_ = conn.Close(closeCtx)
			}()
			result, err := query.Run(ctx, conn, args[0], query.Options{Write: writeMode, Limit: limit, Timeout: timeout})
			if err != nil {
				if errors.Is(err, audit.ErrRefused) {
					return refusedError{err}
				}
				return err
			}
			return write(result)
		},
	})
	command.Flags().BoolVar(&writeMode, "write", false, "Allow a mutating statement after confirmation")
	command.Flags().StringVar(&confirm, "confirm", "", "SHA-256 fingerprint of the exact query")
	command.Flags().IntVar(&limit, "limit", 1000, "Maximum rows to return (1-10000)")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Connection/query timeout (greater than 0, at most 5m)")
	return command
}

func validateSQLArgs(statement string, writeMode bool, confirm string, limit int, timeout time.Duration) error {
	if strings.TrimSpace(statement) == "" || len(statement) > 256*1024 || strings.IndexByte(statement, 0) >= 0 {
		return usageError{errors.New("query must be non-empty, at most 256 KiB, and contain no NUL bytes")}
	}
	if limit < 1 || limit > 10000 {
		return usageError{errors.New("limit must be between 1 and 10000")}
	}
	if timeout <= 0 || timeout > 5*time.Minute {
		return usageError{errors.New("timeout must be greater than 0 and no more than 5m")}
	}
	if !writeMode && confirm != "" {
		return usageError{errors.New("--confirm requires --write")}
	}
	if writeMode {
		if confirm != query.Fingerprint(statement) {
			return refusedError{fmt.Errorf("write SQL requires --confirm %s for this exact statement", query.Fingerprint(statement))}
		}
	}
	return nil
}
