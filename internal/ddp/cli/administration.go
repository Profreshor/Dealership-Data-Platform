package cli

import (
	"fmt"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/auth"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/spf13/cobra"
)

func addAccountAdministration(users *cobra.Command, catalog commandCatalog, registry *string, write func(any) error, noArgs, oneRef cobra.PositionalArgs) {
	users.AddCommand(catalog.declare(commandPolicy{"read", "administrator", "none", "none"}, &cobra.Command{Use: "list", Short: "List portal users, roles and declared permissions", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(*registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		directory, err := auth.New(pool, 0).Directory(cmd.Context(), cfg)
		if err != nil {
			return err
		}
		return write(directory)
	}}))
	var roles []string
	var disabled, clearRoles bool
	update := catalog.declare(commandPolicy{"write", "administrator", "none", "transactional: users.update"}, &cobra.Command{Use: "update <user-id>", Short: "Replace a client user's roles and disabled status; revoke affected sessions", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("disabled") || (len(roles) == 0 && !clearRoles) || (len(roles) > 0 && clearRoles) {
			return usageError{fmt.Errorf("provide --disabled=true|false and either --role or --clear-roles")}
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := auth.New(pool, 0).UpdateAccount(cmd.Context(), args[0], roles, disabled, ""); err != nil {
			return err
		}
		return write(map[string]bool{"changed": true})
	}})
	update.Flags().StringArrayVar(&roles, "role", nil, "Complete set of role IDs; repeat for multiple roles")
	update.Flags().BoolVar(&clearRoles, "clear-roles", false, "Remove every role")
	update.Flags().BoolVar(&disabled, "disabled", false, "Disable or enable the account; must be explicit")
	users.AddCommand(update)
	roleCommands := catalog.declare(localRead, &cobra.Command{Use: "roles", Short: "Manage client permission roles"})
	var name string
	var permissions []string
	var clearPermissions bool
	save := catalog.declare(commandPolicy{"write", "administrator", "none", "transactional: roles.save"}, &cobra.Command{Use: "save <role-id>", Short: "Create or replace a role; revoke sessions when its permissions change", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		if name == "" || (len(permissions) == 0 && !clearPermissions) || (len(permissions) > 0 && clearPermissions) {
			return usageError{fmt.Errorf("provide --name and either --permission or --clear-permissions")}
		}
		cfg, err := config.Load(*registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := auth.New(pool, 0).SaveRole(cmd.Context(), cfg, args[0], name, permissions, ""); err != nil {
			return err
		}
		return write(map[string]bool{"changed": true})
	}})
	save.Flags().StringVar(&name, "name", "", "Role display name")
	save.Flags().StringArrayVar(&permissions, "permission", nil, "Complete set of declared permissions; repeat for multiple permissions")
	save.Flags().BoolVar(&clearPermissions, "clear-permissions", false, "Remove every permission")
	roleCommands.AddCommand(save)
	users.AddCommand(roleCommands)
}
