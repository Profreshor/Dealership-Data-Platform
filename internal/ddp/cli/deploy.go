package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/buildinfo"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/deploy"
	"github.com/spf13/cobra"
)

func deployCommand(catalog commandCatalog, registry *string, write func(any) error, noArgs cobra.PositionalArgs) *cobra.Command {
	root := catalog.declare(localRead, &cobra.Command{Use: "deploy", Short: "Inspect release preflight and record host deployment outcomes"})
	var current, image, failed string
	var outcome deploy.RecordOptions
	for _, action := range []string{"plan", "record", "status"} {
		policy := databaseRead
		if action == "record" {
			policy = commandPolicy{"write", "administrator", "explicit outcome records host deployment evidence", "transactional: ops.deployments and deploy.record audit"}
		}
		command := catalog.declare(policy, &cobra.Command{
			Use: action, Short: map[string]string{"plan": "Check an immutable candidate against this database", "record": "Record a completed host release outcome", "status": "Read the latest host release outcome"}[action],
			Args: noArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				var cfg *config.Config
				var err error
				if action != "status" {
					cfg, err = config.Load(*registry)
					if err != nil {
						return err
					}
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
				defer cancel()
				pool, err := openPool(ctx)
				if err != nil {
					return err
				}
				defer pool.Close()
				conn, err := pool.Acquire(ctx)
				if err != nil {
					return fmt.Errorf("connect to deployment database: %s", safeDBError(err))
				}
				defer conn.Release()
				switch action {
				case "plan":
					result, err := deploy.Plan(ctx, conn.Conn(), cfg, current, image, failed, buildinfo.Version)
					if err != nil {
						return err
					}
					return write(result)
				case "record":
					result, err := deploy.Record(ctx, conn.Conn(), cfg, outcome)
					if err != nil {
						return err
					}
					return write(result)
				case "status":
					result, err := deploy.Latest(ctx, conn.Conn())
					if err != nil {
						return err
					}
					return write(map[string]any{"deployment": result})
				default:
					return errors.New("unknown deployment command")
				}
			},
		})
		switch action {
		case "plan":
			command.Long = "Run from the candidate image. Validate its embedded revision and same-repository immutable references, compare the running and previously failed digests, and inspect embedded migrations against the live database. A pending migration requires backup configuration. This command does not pull images, create a backup, migrate, apply models or restart services. Host automation must perform those steps and verify readiness before recording success."
			command.Flags().StringVar(&current, "current", "", "Running GHCR image digest")
			command.Flags().StringVar(&image, "image", "", "Candidate GHCR image digest")
			command.Flags().StringVar(&failed, "failed", "", "Previously failed GHCR image digest, if any")
			_ = command.MarkFlagRequired("current")
			_ = command.MarkFlagRequired("image")
		case "record":
			command.Long = "Append a bounded release outcome and its audit in one transaction using the owner maintenance credential. The host supplies the exact candidate revision and digest, previous digest, status and phase. Recording success does not itself verify readiness; the host updater must establish that evidence first."
			command.Flags().StringVar(&outcome.Image, "image", "", "Candidate GHCR image digest")
			command.Flags().StringVar(&outcome.PreviousImage, "current", "", "Previous GHCR image digest")
			command.Flags().StringVar(&outcome.Revision, "revision", "", "Candidate's exact embedded Git revision")
			command.Flags().StringVar(&outcome.Status, "status", "", "succeeded or failed")
			command.Flags().StringVar(&outcome.Phase, "phase", "", "preflight, backup, migrations, models, startup, rollback, or ready")
			for _, flag := range []string{"image", "current", "revision", "status", "phase"} {
				_ = command.MarkFlagRequired(flag)
			}
		}
		root.AddCommand(command)
	}
	return root
}
