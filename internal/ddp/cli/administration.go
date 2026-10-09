package cli

import (
	"errors"
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
	var confirmEmail string
	disable := catalog.declare(commandPolicy{"write", "administrator", "operator accounts require --confirm matching the account email", "transactional: users.disable"}, &cobra.Command{Use: "disable <email|user-id>", Short: "Disable any account, including an operator; clear its roles and revoke its sessions", Long: "Disable the account with this email or user ID. Its roles, operator flag, sessions and password links are removed in one audited transaction, so it stops working immediately. An operator account requires --confirm with its exact email; clearing the operator flag lets users bootstrap create a replacement once no operator account remains. Disabling an already disabled account reports no change.", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		result, err := auth.New(pool, 0).DisableAccount(cmd.Context(), args[0], confirmEmail)
		var confirmation auth.ConfirmationError
		if errors.As(err, &confirmation) {
			if confirmation.Operator {
				return refusedError{fmt.Errorf("%s is an operator account; disabling it removes its operator access. Repeat with --confirm %s", confirmation.Email, confirmation.Email)}
			}
			return refusedError{fmt.Errorf("--confirm does not match account %s", confirmation.Email)}
		}
		if err != nil {
			return err
		}
		return write(disabledAccountReport{result, disabledAccountMessage(result)})
	}})
	disable.Flags().StringVar(&confirmEmail, "confirm", "", "Exact email of the account; required for operator accounts")
	users.AddCommand(disable)
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

type disabledAccountReport struct {
	auth.DisabledAccount
	Message string `json:"message"`
}

func disabledAccountMessage(r auth.DisabledAccount) string {
	switch {
	case !r.Changed:
		return fmt.Sprintf("%s was already disabled; no change.", r.Email)
	case r.OperatorRemoved && r.RemainingOperators == 0:
		return fmt.Sprintf("Disabled %s and removed its operator access; its sessions were revoked. No operator account remains: create the dealership's operator with ddp users bootstrap --email <email> and DDP_BOOTSTRAP_PASSWORD set.", r.Email)
	case r.OperatorRemoved:
		return fmt.Sprintf("Disabled %s and removed its operator access; its sessions were revoked. %d other operator account(s) remain; ddp users bootstrap runs only when none remain.", r.Email, r.RemainingOperators)
	default:
		return fmt.Sprintf("Disabled %s, removed its roles and revoked its sessions.", r.Email)
	}
}
