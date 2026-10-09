package cli

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Policies describe required operating authority, not authority granted by the CLI.
// The OS and PostgreSQL grants enforce it; concrete handlers own confirmations
// and transactional audits. Keep each policy beside the handler it describes.
type commandPolicy struct {
	Risk         string `json:"risk"`
	RequiredRole string `json:"required_role"`
	Confirmation string `json:"confirmation"`
	Audit        string `json:"audit"`
}

var localRead = commandPolicy{"read", "local_reader", "none", "none"}
var databaseRead = commandPolicy{"read", "database_reader", "none", "none"}

type commandCatalog map[*cobra.Command]commandPolicy

func (c commandCatalog) declare(policy commandPolicy, cmd *cobra.Command) *cobra.Command {
	c[cmd] = policy
	return cmd
}

func (c commandCatalog) validate(root *cobra.Command) error {
	policy, ok := c[root]
	if !ok || !slices.Contains([]string{"read", "write", "external", "service"}, policy.Risk) ||
		!slices.Contains([]string{"local_reader", "database_reader", "developer", "operator", "administrator", "service"}, policy.RequiredRole) || policy.Confirmation == "" || policy.Audit == "" {
		return fmt.Errorf("command %s has no complete operating declaration", root.CommandPath())
	}
	for _, child := range root.Commands() {
		if err := c.validate(child); err != nil {
			return err
		}
	}
	return nil
}

type flagDescription struct {
	Name        string `json:"name"`
	Shorthand   string `json:"shorthand"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
}

type commandDescription struct {
	Name        string   `json:"name"`
	Usage       string   `json:"usage"`
	Description string   `json:"description"`
	Details     string   `json:"details"`
	Aliases     []string `json:"aliases"`
	Runnable    bool     `json:"runnable"`
	commandPolicy
	Flags    []flagDescription    `json:"flags"`
	Commands []commandDescription `json:"commands"`
}

func (c commandCatalog) describe(cmd *cobra.Command) commandDescription {
	description := commandDescription{
		Name: cmd.CommandPath(), Usage: cmd.UseLine(), Description: cmd.Short, Details: cmd.Long,
		Aliases: append([]string{}, cmd.Aliases...), Runnable: cmd.Runnable(), commandPolicy: c[cmd],
		Flags: []flagDescription{}, Commands: []commandDescription{},
	}
	flags := map[string]*pflag.Flag{}
	cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) { flags[flag.Name] = flag })
	cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) { flags[flag.Name] = flag })
	for _, flag := range flags {
		if flag.Hidden {
			continue
		}
		description.Flags = append(description.Flags, flagDescription{
			Name: flag.Name, Shorthand: flag.Shorthand, Type: flag.Value.Type(), Description: flag.Usage,
			Default: flag.DefValue, Required: slices.Contains(flag.Annotations[cobra.BashCompOneRequiredFlag], "true"),
		})
	}
	slices.SortFunc(description.Flags, func(a, b flagDescription) int { return cmp.Compare(a.Name, b.Name) })
	for _, child := range cmd.Commands() {
		if !child.Hidden {
			description.Commands = append(description.Commands, c.describe(child))
		}
	}
	return description
}

func (c commandCatalog) install(root *cobra.Command, machine *bool, write func(any) error) {
	// Cobra's generated completion commands emit shell text, not our JSON contract.
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if _, declared := c[cmd]; !declared {
			return refusedError{errors.New("command has no operating declaration")}
		}
		return nil
	}
	help := c.declare(localRead, &cobra.Command{Use: "help [command]", Short: "Describe a command and its operating requirements", RunE: func(cmd *cobra.Command, args []string) error {
		target, remaining, err := root.Find(args)
		if err != nil || len(remaining) != 0 {
			return usageError{errors.New("unknown help command")}
		}
		if *machine {
			return write(c.describe(target))
		}
		return target.Help()
	}})
	root.SetHelpCommand(help)
	root.AddCommand(help)
	root.AddCommand(c.declare(localRead, &cobra.Command{Use: "capabilities", Short: "Describe available commands and their required authority; does not grant access", Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.NoArgs(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}, RunE: func(_ *cobra.Command, _ []string) error { return write(c.describe(root)) }}))
	var prepare func(*cobra.Command)
	prepare = func(cmd *cobra.Command) {
		cmd.InitDefaultHelpFlag()
		for _, child := range cmd.Commands() {
			prepare(child)
		}
	}
	prepare(root)
	humanHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if *machine {
			_ = write(c.describe(cmd))
		} else {
			humanHelp(cmd, args)
		}
	})
}
