package cli

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/backup"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/buildinfo"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/spf13/cobra"
)

func backupCommand(catalog commandCatalog, registry *string, write func(any) error, noArgs, oneRef cobra.PositionalArgs) *cobra.Command {
	root := catalog.declare(localRead, &cobra.Command{Use: "backup", Short: "Create and restore encrypted off-host Postgres backups"})
	root.AddCommand(catalog.declare(commandPolicy{"read", "operator", "none", "none"}, &cobra.Command{Use: "list", Short: "List verified off-host archive manifests", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(*registry)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
		defer cancel()
		result, err := backup.List(ctx, cfg)
		if err != nil {
			return err
		}
		return write(result)
	}}))
	var runOptions backup.RunOptions
	var runTimeout time.Duration
	run := catalog.declare(commandPolicy{"external", "administrator", "none; explicit run creates an encrypted backup and expires archives beyond declared retention", "durable: ops.backups and backup.run/backup.expire audit"}, &cobra.Command{Use: "run", Short: "Dump, validate, encrypt and verify an off-host archive", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if runTimeout <= 0 {
			return usageError{errors.New("timeout must be positive")}
		}
		cfg, err := config.Load(*registry)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), runTimeout)
		defer cancel()
		result, err := backup.Run(ctx, cfg, os.Getenv("DATABASE_URL"), runOptions)
		if err != nil {
			return err
		}
		return write(result)
	}})
	run.Flags().StringVar(&runOptions.Revision, "revision", buildinfo.Version, "Exact 40-character client Git revision")
	run.Flags().StringVar(&runOptions.Image, "image", "", "Exact GHCR application image digest")
	run.Flags().BoolVar(&runOptions.IfDue, "if-due", false, "Skip when the declared backup interval has not elapsed")
	run.Flags().DurationVar(&runTimeout, "timeout", 2*time.Hour, "Maximum archive and upload duration")
	root.AddCommand(run)
	var restoreOptions backup.RestoreOptions
	var confirm string
	var restoreTimeout time.Duration
	restore := catalog.declare(commandPolicy{"external", "administrator", "--verify uses a disposable database; a retained target requires --database and matching --confirm", "durable: off-host restore receipts, available source ledger and restored audit"}, &cobra.Command{Use: "restore <backup-id|latest>", Short: "Restore into a new database and verify without running workloads", Long: "Decrypt an archive, preserve its owners/grants, and verify migrations, contracts and declarative queries under API privileges. DATABASE_URL selects the target cluster; component roles must already exist. --verify creates and removes a disposable database. A retained target requires --database and matching --confirm; existing databases are always refused. Failed retained targets remain for inspection. No scheduler, integration, mail or login runs.", Args: func(cmd *cobra.Command, args []string) error {
		if err := oneRef(cmd, args); err != nil {
			return err
		}
		if restoreTimeout <= 0 {
			return usageError{errors.New("timeout must be positive")}
		}
		if restoreOptions.Verify {
			if restoreOptions.Database != "" || confirm != "" {
				return usageError{errors.New("--verify cannot use --database or --confirm")}
			}
		} else if restoreOptions.Database == "" || confirm != restoreOptions.Database {
			return refusedError{errors.New("restore requires --database and matching --confirm, or --verify")}
		}
		if restoreOptions.IfDue && !restoreOptions.Verify {
			return usageError{errors.New("--if-due requires --verify")}
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(*registry)
		if err != nil {
			return err
		}
		restoreOptions.ID = args[0]
		ctx, cancel := context.WithTimeout(cmd.Context(), restoreTimeout)
		defer cancel()
		result, err := backup.Restore(ctx, cfg, os.Getenv("DATABASE_URL"), restoreOptions)
		if err != nil {
			return err
		}
		return write(result)
	}})
	restore.Flags().StringVar(&restoreOptions.Database, "database", "", "New database to retain after successful restore")
	restore.Flags().StringVar(&confirm, "confirm", "", "Confirm the exact new database name")
	restore.Flags().BoolVar(&restoreOptions.Verify, "verify", false, "Verify in an automatically removed disposable database")
	restore.Flags().BoolVar(&restoreOptions.IfDue, "if-due", false, "Skip if a successful restore test is less than seven days old")
	restore.Flags().DurationVar(&restoreTimeout, "timeout", 2*time.Hour, "Maximum download, restore and verification duration")
	root.AddCommand(restore)
	return root
}
