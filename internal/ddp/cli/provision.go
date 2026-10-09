package cli

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/spf13/cobra"
)

func provisionCommand(catalog commandCatalog, write func(any) error, oneRef cobra.PositionalArgs) *cobra.Command {
	return catalog.declare(commandPolicy{"write", "administrator", "explicit component provisions or rotates its login", "transactional: database.provision"}, &cobra.Command{
		Use: "provision <owner|scheduler|job|api|backup|readonly>", Short: "Create or rotate a restricted production database login",
		Long: "After initial migrations, use administrator DATABASE_URL and <COMPONENT>_DATABASE_PASSWORD to provision one fixed ddp_<component>_login. Passwords must be 64 lowercase hexadecimal characters (openssl rand -hex 32). Groups stay NOLOGIN. Existing unexpected privileges are refused. Restart affected services after rotating their host environment value. Owner and administrator credentials belong only in host maintenance commands.",
		Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), time.Minute)
			defer cancel()
			result, err := migrate.Provision(ctx, os.Getenv("DATABASE_URL"), args[0], os.Getenv(strings.ToUpper(args[0])+"_DATABASE_PASSWORD"))
			if err != nil {
				return err
			}
			return write(result)
		},
	})
}
