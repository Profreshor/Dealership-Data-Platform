package cli

import (
	"errors"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/onboard"
	"github.com/spf13/cobra"
)

func initCommand(catalog commandCatalog, write func(any) error) *cobra.Command {
	var discovery, template string
	var dryRun bool
	command := catalog.declare(commandPolicy{"write", "developer", "none", "local files"}, &cobra.Command{
		Use: "init <directory>", Short: "Initialize a client project from discovery facts", Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return usageError{err}
			}
			if discovery == "" {
				return usageError{errors.New("--discovery is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := onboard.Initialize(cmd.Context(), onboard.Options{Template: template, Directory: args[0], Discovery: discovery, DryRun: dryRun})
			if err != nil {
				return err
			}
			return write(result)
		},
	})
	command.Flags().StringVar(&discovery, "discovery", "", "Structured discovery YAML path")
	command.Flags().StringVar(&template, "template", ".", "Local template checkout")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "Preview files without writing")
	return command
}
