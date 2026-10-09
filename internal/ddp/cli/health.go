package cli

import (
	"fmt"
	"path/filepath"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/spf13/cobra"
)

func healthCommand(catalog commandCatalog, registry *string, write func(any) error, noArgs, oneRef cobra.PositionalArgs) *cobra.Command {
	list := func(cmd *cobra.Command, _ []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		rows, err := health.Latest(cmd.Context(), pool)
		if err != nil {
			return err
		}
		return write(rows)
	}
	command := catalog.declare(databaseRead, &cobra.Command{Use: "health", Short: "Inspect persisted health evaluations", Args: noArgs, RunE: list})
	command.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "list", Short: "Read the latest evaluation of each check", Args: noArgs, RunE: list}))
	command.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "show <check-ref>", Short: "Read one check's latest evaluation", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		rows, err := health.Latest(cmd.Context(), pool)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Ref == args[0] {
				return write(row)
			}
		}
		return fmt.Errorf("health check not found")
	}}))
	command.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "alerts", Short: "Read the latest 100 alert and recovery records", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		rows, err := health.Alerts(cmd.Context(), pool)
		if err != nil {
			return err
		}
		return write(rows)
	}}))
	command.AddCommand(catalog.declare(commandPolicy{"external", "operator", "none", "transactional: jobs.run; health and alerts persisted separately"}, &cobra.Command{Use: "evaluate", Short: "Run a durable health evaluation and enqueue transition notices", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(*registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		run, err := jobs.Run(cmd.Context(), pool, cfg, filepath.Dir(*registry), jobs.HealthRef)
		if err != nil {
			return err
		}
		return write(run)
	}}))
	return command
}
