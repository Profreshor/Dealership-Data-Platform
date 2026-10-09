package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/metrics"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/service"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
	"github.com/spf13/cobra"
)

func serveCommand(catalog commandCatalog, registry *string, write func(any) error, logs io.Writer, noArgs cobra.PositionalArgs, register func(*web.Registry)) *cobra.Command {
	var all bool
	var options service.Options
	command := catalog.declare(commandPolicy{"service", "service", "--all starts API and scheduler with separate database credentials", "operational: sessions, HTTP logs and scheduler execution history"}, &cobra.Command{
		Use: "serve --all", Short: "Run the API and scheduler in one process",
		Long: "Run the API with DATABASE_URL and the scheduler with SCHEDULER_DATABASE_URL. Python jobs use JOB_DATABASE_URL. A service failure stops both loops. Migrations and Vite remain separate commands. Use separate api and scheduler processes in production Compose.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := noArgs(cmd, args); err != nil {
				return err
			}
			if !all {
				return usageError{errors.New("serve requires --all; use api or scheduler for a single service")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*registry)
			if err != nil {
				return err
			}
			options.APIURL = os.Getenv("DATABASE_URL")
			options.SchedulerURL = os.Getenv("SCHEDULER_DATABASE_URL")
			if options.APIURL == "" || options.SchedulerURL == "" {
				return errors.New("DATABASE_URL and SCHEDULER_DATABASE_URL are required")
			}
			if err := service.RunAll(cmd.Context(), cfg, filepath.Dir(*registry), options, logs, register); err != nil {
				return err
			}
			return write(map[string]string{"state": "stopped"})
		},
	})
	command.Flags().BoolVar(&all, "all", false, "Start both API and scheduler")
	command.Flags().StringVar(&options.APIMetricsAddr, "api-metrics-addr", metrics.APIAddr, "Private API metrics listen address")
	command.Flags().StringVar(&options.SchedulerMetricsAddr, "scheduler-metrics-addr", metrics.SchedulerAddr, "Private scheduler metrics listen address")
	return command
}
