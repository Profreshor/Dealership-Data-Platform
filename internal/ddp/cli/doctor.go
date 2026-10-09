package cli

import (
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/doctor"
	"github.com/spf13/cobra"
)

// The report has already been rendered; this sentinel selects exit 3 without
// emitting a second JSON envelope.
type unhealthyReport struct{}

func (unhealthyReport) Error() string { return "reported unhealthy state or configuration drift" }

func doctorCommand(catalog commandCatalog, registry *string, write func(any) error, noArgs cobra.PositionalArgs) *cobra.Command {
	return catalog.declare(commandPolicy{"read", "local_reader", "none", "none: live host, database and SMTP probes"}, &cobra.Command{Use: "doctor", Short: "Check live platform and host state with concrete repairs", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		report := doctor.Run(cmd.Context(), *registry)
		if err := write(report); err != nil {
			return err
		}
		if report.State != "ok" {
			return unhealthyReport{}
		}
		return nil
	}})
}
