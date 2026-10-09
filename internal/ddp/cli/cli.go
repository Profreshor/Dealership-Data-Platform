package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/buildinfo"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/dev"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/inspect"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/jobs"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/metrics"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/models"
	projectregistry "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/registry"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/render"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/scaffold"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/scheduler"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/service"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/serving"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/smoke"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/tui"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
	"github.com/charmbracelet/x/term"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

type usageError struct{ error }
type refusedError struct{ error }

func Execute(ctx context.Context, args []string, out, errOut io.Writer, register func(*web.Registry)) int {
	catalog := commandCatalog{}
	var machine bool
	var registry string
	root := catalog.declare(commandPolicy{"external", "database_reader", "TUI job actions require typed confirmation and operator database privileges", "delegated: jobs.run"}, &cobra.Command{Use: "ddp", Short: "Operate a DDP client data system", Long: "With no subcommand in a terminal, open the operational TUI. Piped output and --json show help. TUI reads share CLI inspection packages; job actions execute jobs run with the same credentials, confirmation checks and audit path.", SilenceUsage: true, SilenceErrors: true})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	root.PersistentFlags().BoolVar(&machine, "json", false, "Emit versioned JSON")
	root.PersistentFlags().StringVar(&registry, "config", "ddp.yaml", "Project registry path")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	write := func(data any) error { return render.Write(out, machine, data, nil) }
	noArgs := func(cmd *cobra.Command, args []string) error {
		if err := cobra.NoArgs(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
	oneRef := func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
	root.AddCommand(commsCommand(catalog, &registry, write, noArgs, oneRef))
	root.AddCommand(healthCommand(catalog, &registry, write, noArgs, oneRef))
	root.AddCommand(doctorCommand(catalog, &registry, write, noArgs))
	root.AddCommand(checkCommand(catalog, &registry, write, errOut, noArgs))
	root.AddCommand(sqlCommand(catalog, write, oneRef))
	root.AddCommand(backupCommand(catalog, &registry, write, noArgs, oneRef))
	root.AddCommand(provisionCommand(catalog, write, oneRef))
	root.AddCommand(deployCommand(catalog, &registry, write, noArgs))
	root.AddCommand(initCommand(catalog, write))
	root.AddCommand(serveCommand(catalog, &registry, write, errOut, noArgs, register))
	version := catalog.declare(localRead, &cobra.Command{Use: "version", Short: "Show the build version", Args: noArgs, RunE: func(_ *cobra.Command, _ []string) error {
		return write(map[string]string{"version": buildinfo.Version})
	}})
	validate := func(_ *cobra.Command, _ []string) error {
		c, err := config.Load(registry)
		if err != nil {
			return err
		}
		if _, err := serving.Routes(c, register); err != nil {
			return err
		}
		return write(map[string]any{"valid": true, "project": c.Ddp.Name, "resources": len(c.References())})
	}
	conf := catalog.declare(localRead, &cobra.Command{Use: "config", Short: "Inspect and validate project configuration"})
	conf.AddCommand(configDiffCommand(catalog, &registry, write))
	conf.AddCommand(catalog.declare(localRead, &cobra.Command{Use: "validate", Short: "Validate registry and entrypoints", Args: noArgs, RunE: validate}))
	conf.AddCommand(catalog.declare(localRead, &cobra.Command{Use: "schema", Short: "Print the generated JSON Schema", Args: noArgs, RunE: func(_ *cobra.Command, _ []string) error {
		if machine {
			return write(config.Schema())
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(config.Schema())
	}}))
	migration := catalog.declare(localRead, &cobra.Command{Use: "migrate", Short: "Inspect and apply embedded migrations"})
	for _, action := range []string{"up", "status"} {
		migrationPolicy := databaseRead
		if action == "up" {
			migrationPolicy = commandPolicy{"write", "administrator", "none", "transactional: migrate.up; failed attempts recorded after rollback"}
		}
		migration.AddCommand(catalog.declare(migrationPolicy, &cobra.Command{Use: action, Short: action + " migrations", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			url := os.Getenv("DATABASE_URL")
			if url == "" {
				return fmt.Errorf("DATABASE_URL is required")
			}
			conn, err := pgx.Connect(cmd.Context(), url)
			if err != nil {
				return fmt.Errorf("connect to database: %s", safeDBError(err))
			}
			defer conn.Close(context.Background())
			if action == "up" {
				if err := migrate.Up(cmd.Context(), conn); err != nil {
					return err
				}
			}
			entries, err := migrate.Status(cmd.Context(), conn)
			if err != nil {
				return err
			}
			return write(entries)
		}}))
	}
	root.AddCommand(version, conf, migration, catalog.declare(localRead, &cobra.Command{Use: "validate", Short: "Validate project declarations", Args: noArgs, RunE: validate}))
	loadRegistry := func() (*projectregistry.Registry, error) {
		cfg, err := config.Load(registry)
		if err != nil {
			return nil, err
		}
		return projectregistry.Build(cfg)
	}
	root.AddCommand(catalog.declare(localRead, &cobra.Command{Use: "registry", Short: "List declared resource types, configuration and relationships", Args: noArgs, RunE: func(_ *cobra.Command, _ []string) error {
		declared, err := loadRegistry()
		if err != nil {
			return err
		}
		return write(declared)
	}}))
	root.AddCommand(catalog.declare(localRead, &cobra.Command{Use: "search <term>", Short: "Search declared resources and their configuration", Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return usageError{err}
		}
		if strings.TrimSpace(args[0]) == "" {
			return usageError{fmt.Errorf("search term must not be empty")}
		}
		return nil
	}, RunE: func(_ *cobra.Command, args []string) error {
		declared, err := loadRegistry()
		if err != nil {
			return err
		}
		return write(declared.Search(args[0]))
	}}))
	root.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "inspect <ref>", Short: "Inspect declared relationships and observed database facts", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := inspect.Inspect(cmd.Context(), pool, cfg, args[0])
		if err != nil {
			return err
		}
		return write(result)
	}}))
	root.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "diagnose <ref>", Short: "Join declared lineage to recorded failures and health evidence", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := inspect.Diagnose(cmd.Context(), pool, cfg, args[0])
		if err != nil {
			return err
		}
		return write(result)
	}}))
	root.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "status", Short: "Read scheduler, job, health and outbox observations", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := inspect.Overview(cmd.Context(), pool, cfg)
		if err != nil {
			return err
		}
		return write(result)
	}}))
	for _, kind := range []string{"tables", "integrations"} {
		list := func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(registry)
			if err != nil {
				return err
			}
			pool, err := openPool(cmd.Context())
			if err != nil {
				return err
			}
			defer pool.Close()
			if kind == "tables" {
				result, err := inspect.ListTables(cmd.Context(), pool, cfg)
				if err != nil {
					return err
				}
				return write(result)
			}
			result, err := inspect.ListIntegrations(cmd.Context(), pool, cfg)
			if err != nil {
				return err
			}
			return write(result)
		}
		commands := catalog.declare(databaseRead, &cobra.Command{Use: kind, Short: "Inspect registered " + kind, Args: noArgs, RunE: list})
		commands.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "list", Short: "List declared " + kind + " and observed facts", Args: noArgs, RunE: list}))
		commands.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "show <ref>", Short: "Inspect one registered resource", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(registry)
			if err != nil {
				return err
			}
			pool, err := openPool(cmd.Context())
			if err != nil {
				return err
			}
			defer pool.Close()
			if kind == "tables" {
				result, err := inspect.ShowTable(cmd.Context(), pool, cfg, args[0])
				if err != nil {
					return err
				}
				return write(result)
			}
			result, err := inspect.ShowIntegration(cmd.Context(), pool, cfg, args[0])
			if err != nil {
				return err
			}
			return write(result)
		}}))
		root.AddCommand(commands)
	}
	create := catalog.declare(localRead, &cobra.Command{Use: "new", Short: "Create registered client artifacts from explicit definitions"})
	for _, kind := range []string{"job", "model", "integration", "endpoint", "page", "health", "migration", "route"} {
		var definitionPath, sourcePath string
		var dryRun bool
		command := catalog.declare(commandPolicy{"write", "developer", "--dry-run previews without writing", "none: local files"}, &cobra.Command{Use: kind + " <name>", Short: "Create a registered " + kind + " scaffold", Args: oneRef, RunE: func(_ *cobra.Command, args []string) error {
			request := scaffold.Request{Kind: kind, Name: args[0]}
			if definitionPath != "" {
				data, err := os.ReadFile(definitionPath)
				if err != nil {
					return fmt.Errorf("read definition: %w", err)
				}
				request.Definition = data
			}
			if sourcePath != "" {
				if kind == "route" {
					return usageError{fmt.Errorf("route does not accept --source")}
				}
				data, err := os.ReadFile(sourcePath)
				if err != nil {
					return fmt.Errorf("read source: %w", err)
				}
				request.Source = data
			}
			if dryRun {
				result, err := scaffold.Preview(registry, request)
				if err != nil {
					return err
				}
				files := make(map[string]string, len(result.Files))
				for path, source := range result.Files {
					files[path] = string(source)
				}
				return write(struct {
					Kind     string            `json:"kind"`
					Ref      string            `json:"ref"`
					Registry string            `json:"registry"`
					Files    map[string]string `json:"files"`
				}{result.Kind, result.Ref, string(result.Registry), files})
			}
			result, err := scaffold.Apply(registry, request)
			if err != nil {
				return err
			}
			return write(result)
		}})
		command.Flags().StringVar(&definitionPath, "definition", "", "YAML mapping containing the resource's declared facts")
		command.Flags().StringVar(&sourcePath, "source", "", "Source file to copy into the scaffold (required for SQL models and migrations)")
		command.Flags().BoolVar(&dryRun, "dry-run", false, "Show the proposed registry and files without writing")
		create.AddCommand(command)
	}
	root.AddCommand(create)
	root.AddCommand(catalog.declare(localRead, &cobra.Command{Use: "routes", Short: "List registered HTTP routes and their enforced policies", Args: noArgs, RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		routes, err := serving.Routes(cfg, register)
		if err != nil {
			return err
		}
		return write(routes)
	}}))
	var apiMetricsAddr, schedulerMetricsAddr string
	apiCommand := catalog.declare(commandPolicy{"service", "service", "none", "operational: sessions and HTTP logs"}, &cobra.Command{Use: "api", Short: "Serve the protected portal and API", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		url := os.Getenv("DATABASE_URL")
		if url == "" {
			return fmt.Errorf("DATABASE_URL is required")
		}
		if err := serving.Run(cmd.Context(), cfg, url, apiMetricsAddr, register); err != nil {
			return err
		}
		return write(map[string]string{"state": "stopped"})
	}})
	apiCommand.Flags().StringVar(&apiMetricsAddr, "metrics-addr", metrics.APIAddr, "Private metrics listen address; expose only to a trusted network")
	root.AddCommand(apiCommand)
	schedulerCommand := catalog.declare(commandPolicy{"service", "service", "none", "operational: scheduler execution history"}, &cobra.Command{Use: "scheduler", Short: "Run the durable job scheduler", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		url := os.Getenv("DATABASE_URL")
		if url == "" {
			return fmt.Errorf("DATABASE_URL is required")
		}
		if err := service.RunScheduler(cmd.Context(), cfg, url, filepath.Dir(registry), schedulerMetricsAddr, errOut); err != nil {
			return err
		}
		return write(map[string]string{"state": "stopped"})
	}})
	schedulerCommand.Flags().StringVar(&schedulerMetricsAddr, "metrics-addr", metrics.SchedulerAddr, "Private metrics listen address; expose only to a trusted network")
	schedulerCommand.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "status", Short: "Read scheduler heartbeat and lock ownership", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		state, err := scheduler.Status(cmd.Context(), pool)
		if err != nil {
			return err
		}
		return write(state)
	}}))
	root.AddCommand(schedulerCommand)
	jobCommands := catalog.declare(localRead, &cobra.Command{Use: "jobs", Short: "Run registered jobs"})
	var confirmIdempotency []string
	var withDownstream bool
	runJob := catalog.declare(commandPolicy{"external", "operator", "side effects require matching --confirm-idempotency", "transactional: jobs.run"}, &cobra.Command{Use: "run <job/name|model/name|ddp:comms_relay|ddp:health|ddp:cleanup>", Short: "Run one registered job", Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		refs, err := scheduler.SelectedJobs(cfg, args[0], withDownstream)
		if err != nil {
			return usageError{err}
		}
		if err := scheduler.ConfirmStrategies(cfg, refs, confirmIdempotency); err != nil {
			return refusedError{fmt.Errorf("operation requires --confirm-idempotency: %w", err)}
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		if withDownstream {
			queued, err := scheduler.QueueRun(cmd.Context(), pool, cfg, args[0], true, confirmIdempotency)
			if err != nil {
				return err
			}
			return write(queued)
		}
		run, err := jobs.Run(cmd.Context(), pool, cfg, filepath.Dir(registry), args[0])
		if err != nil {
			return fmt.Errorf("job execution %s: %w", run.ID, err)
		}
		return write(run)
	}})
	runJob.Flags().StringSliceVar(&confirmIdempotency, "confirm-idempotency", nil, "Confirm a new side-effecting execution by repeating its declared strategy")
	runJob.Flags().BoolVar(&withDownstream, "with-downstream", false, "Queue this job and its downstream chain for the scheduler")
	jobCommands.AddCommand(runJob)
	jobCommands.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "list", Short: "List jobs, schedules and current execution state", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := inspect.ListJobs(cmd.Context(), pool, cfg)
		if err != nil {
			return err
		}
		return write(result)
	}}))
	jobCommands.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "show <job/name|model/name|ddp:comms_relay|ddp:health|ddp:cleanup>", Short: "Show one job and its latest execution", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := inspect.ShowJob(cmd.Context(), pool, cfg, args[0])
		if err != nil {
			return err
		}
		return write(result)
	}}))
	for _, action := range []string{"pause", "resume"} {
		jobCommands.AddCommand(catalog.declare(commandPolicy{"write", "operator", "none", "transactional: ddp.audit"}, &cobra.Command{Use: action + " <job/name|model/name|ddp:comms_relay|ddp:health|ddp:cleanup>", Short: action + " future job executions", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(registry)
			if err != nil {
				return err
			}
			pool, err := openPool(cmd.Context())
			if err != nil {
				return err
			}
			defer pool.Close()
			result, err := scheduler.SetPaused(cmd.Context(), pool, cfg, args[0], action == "pause")
			if err != nil {
				return err
			}
			return write(result)
		}}))
	}
	var backfillFrom, backfillThrough string
	var backfillApply, backfillDownstream bool
	var backfillConfirm int64
	var backfillStrategies []string
	backfill := catalog.declare(commandPolicy{"external", "operator", "preview by default; --apply requires exact --confirm-executions and side-effect strategies", "transactional: jobs.backfill on apply"}, &cobra.Command{Use: "backfill <job/name|model/name>", Short: "Preview historical occurrences; --apply queues the confirmed count", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		from, err := time.Parse(time.RFC3339, backfillFrom)
		if err != nil {
			return usageError{fmt.Errorf("--from must be an RFC3339 timestamp with timezone")}
		}
		through, err := time.Parse(time.RFC3339, backfillThrough)
		if err != nil {
			return usageError{fmt.Errorf("--through must be an RFC3339 timestamp with timezone")}
		}
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		preview, err := scheduler.BackfillPlan(cfg, args[0], from, through, backfillDownstream)
		if err != nil {
			return usageError{err}
		}
		if !backfillApply {
			return write(preview)
		}
		if backfillConfirm != preview.Executions {
			return refusedError{fmt.Errorf("backfill requires --confirm-executions %d", preview.Executions)}
		}
		if err := scheduler.ConfirmStrategies(cfg, preview.Jobs, backfillStrategies); err != nil {
			return refusedError{fmt.Errorf("backfill requires --confirm-idempotency: %w", err)}
		}
		_, _ = fmt.Fprintf(errOut, "backfill occurrences=%d executions=%d state=starting\n", preview.Occurrences, preview.Executions)
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := scheduler.Backfill(cmd.Context(), pool, cfg, args[0], from, through, backfillDownstream, backfillStrategies)
		if err != nil {
			return err
		}
		return write(result)
	}})
	backfill.Flags().StringVar(&backfillFrom, "from", "", "Inclusive historical RFC3339 start")
	backfill.Flags().StringVar(&backfillThrough, "through", "", "Inclusive historical RFC3339 end")
	backfill.Flags().BoolVar(&backfillApply, "apply", false, "Queue the previewed executions")
	backfill.Flags().Int64Var(&backfillConfirm, "confirm-executions", -1, "Repeat the previewed execution count")
	backfill.Flags().BoolVar(&backfillDownstream, "with-downstream", false, "Include each occurrence's downstream chain")
	backfill.Flags().StringSliceVar(&backfillStrategies, "confirm-idempotency", nil, "Repeat every side-effect strategy in the preview")
	jobCommands.AddCommand(backfill)
	root.AddCommand(jobCommands)
	var runLimit, logLimit int
	var runJobRef string
	runs := catalog.declare(localRead, &cobra.Command{Use: "runs", Short: "Inspect durable executions and attempts"})
	listRuns := catalog.declare(databaseRead, &cobra.Command{Use: "list", Short: "List recent executions", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := inspect.ListRuns(cmd.Context(), pool, runJobRef, runLimit)
		if err != nil {
			return err
		}
		return write(result)
	}})
	listRuns.Flags().IntVar(&runLimit, "limit", 50, "Maximum executions (1–100)")
	listRuns.Flags().StringVar(&runJobRef, "job", "", "Filter by job/<name>")

	runs.AddCommand(listRuns, catalog.declare(databaseRead, &cobra.Command{Use: "show execution/<id>", Short: "Show an execution and its recent attempts", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := inspect.ShowRun(cmd.Context(), pool, args[0])
		if err != nil {
			return err
		}
		return write(result)
	}}))
	runs.AddCommand(catalog.declare(commandPolicy{"write", "operator", "none", "transactional: ddp.audit"}, &cobra.Command{Use: "cancel execution/<id>", Short: "Request cancellation and preserve execution history", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := scheduler.Cancel(cmd.Context(), pool, args[0])
		if err != nil {
			return err
		}
		return write(result)
	}}))
	logs := catalog.declare(databaseRead, &cobra.Command{Use: "logs <execution/id|job/name>", Short: "Read stored output from recent attempts", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := inspect.Logs(cmd.Context(), pool, args[0], logLimit)
		if err != nil {
			return err
		}
		return write(result)
	}})
	logs.Flags().IntVar(&logLimit, "limit", 50, "Maximum attempts (1–100)")
	root.AddCommand(runs, logs)
	modelCommands := catalog.declare(localRead, &cobra.Command{Use: "models", Short: "Plan, apply and verify registered SQL models"})
	for _, action := range []string{"plan", "apply", "verify", "refresh"} {
		modelPolicy := databaseRead
		if action == "apply" || action == "refresh" {
			modelPolicy = commandPolicy{"write", "operator", "none", "transactional: models.apply or models.refresh with model history"}
		}
		use := action
		args := noArgs
		if action == "refresh" {
			use += " model/<name>"
			args = func(cmd *cobra.Command, args []string) error {
				if err := cobra.ExactArgs(1)(cmd, args); err != nil {
					return usageError{err}
				}
				return nil
			}
		}
		modelCommands.AddCommand(catalog.declare(modelPolicy, &cobra.Command{Use: use, Short: action + " registered SQL models", Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(registry)
			if err != nil {
				return err
			}
			pool, err := openPool(cmd.Context())
			if err != nil {
				return err
			}
			defer pool.Close()
			var result any
			switch action {
			case "plan":
				result, err = models.Plan(cmd.Context(), pool, cfg, filepath.Dir(registry))
			case "apply":
				result, err = models.Apply(cmd.Context(), pool, cfg, filepath.Dir(registry))
			case "refresh":
				result, err = models.Refresh(cmd.Context(), pool, cfg, filepath.Dir(registry), args[0])
			case "verify":
				err = models.Verify(cmd.Context(), pool, cfg)
				result = map[string]bool{"valid": err == nil}
			}
			if err != nil {
				return err
			}
			return write(result)
		}}))
	}
	root.AddCommand(modelCommands)
	var email string
	users := catalog.declare(localRead, &cobra.Command{Use: "users", Short: "Manage local portal accounts"})
	bootstrap := catalog.declare(commandPolicy{"write", "administrator", "none", "transactional: users.bootstrap"}, &cobra.Command{Use: "bootstrap", Short: "Create the first operator using DDP_BOOTSTRAP_PASSWORD", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if email == "" {
			return usageError{fmt.Errorf("--email is required")}
		}
		password := os.Getenv("DDP_BOOTSTRAP_PASSWORD")
		if password == "" {
			return fmt.Errorf("DDP_BOOTSTRAP_PASSWORD is required")
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := auth.New(pool, 8*time.Hour).Bootstrap(cmd.Context(), email, password); err != nil {
			return err
		}
		return write(map[string]bool{"created": true})
	}})
	bootstrap.Flags().StringVar(&email, "email", "", "Initial operator email")
	users.AddCommand(bootstrap)
	var inviteEmail string
	var roles []string
	invite := catalog.declare(commandPolicy{"external", "administrator", "none", "transactional: users.invite"}, &cobra.Command{Use: "invite", Short: "Invite a non-administrator portal user", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if inviteEmail == "" {
			return usageError{fmt.Errorf("--email is required")}
		}
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		invitation, err := auth.New(pool, 8*time.Hour).Invite(cmd.Context(), cfg, inviteEmail, roles, "")
		if err != nil {
			return err
		}
		return write(invitation)
	}})
	invite.Flags().StringVar(&inviteEmail, "email", "", "Invitee email")
	invite.Flags().StringArrayVar(&roles, "role", nil, "Role ID to grant; repeat for multiple roles")
	users.AddCommand(invite)
	addAccountAdministration(users, catalog, &registry, write, noArgs, oneRef)
	root.AddCommand(users)
	root.AddCommand(catalog.declare(commandPolicy{"external", "developer", "none", "operational: disposable smoke records"}, &cobra.Command{Use: "smoke", Short: "Create a disposable test database and verify ingestion through the browser", Long: "Run the source-checkout acceptance gate using TEST_DATABASE_URL and declared integration secrets. Requires installed Python dependencies, Node, Playwright Chromium and a built portal. Creates and removes its own database and temporary job login; runs the selected ingestion job against its declared integration.", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
		defer cancel()
		report, err := smoke.Run(ctx, cfg, registry, errOut, register)
		if err != nil {
			return err
		}
		return write(report)
	}}))
	var devOptions dev.Options
	devCommand := catalog.declare(commandPolicy{"service", "developer", "--scheduler enables scheduled work; --comms also enables SMTP delivery", "operational: local runtime logs and opted-in execution history"}, &cobra.Command{
		Use: "dev", Short: "Start local Postgres, HTTP and Vite; opt in to scheduling and email",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := noArgs(cmd, args); err != nil {
				return err
			}
			if devOptions.Comms && !devOptions.Scheduler {
				return usageError{errors.New("--comms requires --scheduler")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := config.Load(registry)
			if err != nil {
				return err
			}
			if err := dev.Run(cmd.Context(), c, filepath.Dir(registry), errOut, register, devOptions); err != nil {
				return err
			}
			return write(map[string]string{"state": "stopped"})
		},
	})
	devCommand.Flags().BoolVar(&devOptions.Scheduler, "scheduler", false, "Enable scheduled work against development Postgres")
	devCommand.Flags().BoolVar(&devOptions.Comms, "comms", false, "Enable configured SMTP delivery; requires --scheduler")
	devCommand.Flags().StringVar(&devOptions.APIMetricsAddr, "api-metrics-addr", metrics.APIAddr, "Private API metrics listen address")
	devCommand.Flags().StringVar(&devOptions.SchedulerMetricsAddr, "scheduler-metrics-addr", metrics.SchedulerAddr, "Private scheduler metrics listen address")
	root.AddCommand(devCommand)
	root.Args = noArgs
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		file, ok := out.(*os.File)
		if machine || !ok || !term.IsTerminal(file.Fd()) || !term.IsTerminal(os.Stdin.Fd()) {
			return cmd.Help()
		}
		cfg, err := config.Load(registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return tui.Run(cmd.Context(), pool, cfg, executable, registry, os.Stdin, out)
	}
	catalog.install(root, &machine, write)
	err := catalog.validate(root)
	if err == nil {
		err = root.ExecuteContext(ctx)
	}
	if _, ok := err.(unhealthyReport); ok {
		return 3
	}
	if err == nil {
		return 0
	}
	code := 1
	name := "error"
	if _, ok := err.(usageError); ok || strings.HasPrefix(err.Error(), "unknown command") {
		code = 2
		name = "usage"
	}
	if _, ok := err.(refusedError); ok || errors.Is(err, audit.ErrRefused) {
		code = 4
		name = "refused"
	}
	// Flag parsing may fail before reaching --json; preserve machine errors regardless of order.
	for _, arg := range args {
		if arg == "--json" || arg == "--json=true" {
			machine = true
		}
	}
	dst := errOut
	if machine {
		dst = out
	}
	_ = render.Write(dst, machine, nil, &render.Error{Code: name, Message: err.Error()})
	return code
}

func openPool(ctx context.Context) (*pgxpool.Pool, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_URL")
	}
	return pool, nil
}

func safeDBError(err error) string {
	// pgx connection errors can include the connection string; credentials stay out of CLI output.
	if strings.Contains(err.Error(), "password") {
		return "authentication failed or invalid connection configuration"
	}
	return "connection failed; check DATABASE_URL and database availability"
}
