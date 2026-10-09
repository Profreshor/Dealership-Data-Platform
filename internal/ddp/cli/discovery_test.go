package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func discovery(t *testing.T, args ...string) commandDescription {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := Execute(t.Context(), append(args, "--json"), &out, &errOut, nil); code != 0 {
		t.Fatalf("%v: %d %s %s", args, code, &out, &errOut)
	}
	var response struct {
		Version int                `json:"version"`
		OK      bool               `json:"ok"`
		Data    commandDescription `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.OK || response.Version != 1 || errOut.Len() != 0 {
		t.Fatalf("invalid discovery: %s %s %v", &out, &errOut, err)
	}
	return response.Data
}

func TestDiscoveryUsesCompleteRegisteredTreeWithoutRuntime(t *testing.T) {
	t.Setenv("DATABASE_URL", "not a database")
	t.Setenv("DDP_BOOTSTRAP_PASSWORD", "never-serialize-this-password")
	root := discovery(t, "capabilities", "--config", "does-not-exist")
	if !reflect.DeepEqual(root, discovery(t, "help")) || !reflect.DeepEqual(root, discovery(t, "--help")) {
		t.Fatal("help and capabilities disagree")
	}
	seen := map[string]commandDescription{}
	var visit func(commandDescription)
	visit = func(cmd commandDescription) {
		if cmd.Name == "" || cmd.Risk == "" || cmd.RequiredRole == "" || cmd.Confirmation == "" || cmd.Audit == "" || cmd.Commands == nil || cmd.Flags == nil || cmd.Aliases == nil {
			t.Fatalf("incomplete declaration: %+v", cmd)
		}
		if _, duplicate := seen[cmd.Name]; duplicate {
			t.Fatalf("duplicate command %s", cmd.Name)
		}
		seen[cmd.Name] = cmd
		flags := map[string]bool{}
		for _, flag := range cmd.Flags {
			if flags[flag.Name] {
				t.Fatalf("duplicate flag %s on %s", flag.Name, cmd.Name)
			}
			flags[flag.Name] = true
		}
		for _, flag := range []string{"json", "config", "help"} {
			if !flags[flag] {
				t.Fatalf("missing flag %s on %s", flag, cmd.Name)
			}
		}
		for _, child := range cmd.Commands {
			visit(child)
		}
	}
	visit(root)
	for _, path := range []string{
		"api", "capabilities", "comms enqueue", "comms list", "comms preview", "comms relay", "comms resend", "comms retry", "comms show", "comms test-send",
		"check", "config diff", "config schema", "config validate", "dev", "diagnose", "doctor", "health", "health alerts", "health evaluate", "health list", "health show", "help", "inspect",
		"integrations", "integrations list", "integrations show", "jobs backfill", "jobs list", "jobs pause", "jobs resume", "jobs run", "jobs show", "logs",
		"migrate status", "migrate up", "models apply", "models plan", "models refresh", "models verify", "new endpoint", "new health", "new integration", "new job", "new migration", "new model", "new page", "new route",
		"registry", "routes", "runs cancel", "runs list", "runs show", "scheduler", "scheduler status", "search", "serve", "smoke", "sql", "status", "tables", "tables list", "tables show", "users bootstrap", "validate", "version",
	} {
		if _, exists := seen["ddp "+path]; !exists {
			t.Errorf("missing command %s", path)
		}
	}
	if _, exists := seen["ddp completion"]; exists {
		t.Fatal("undeclared shell completion exposed")
	}
	for _, path := range []string{"jobs run", "jobs backfill", "comms resend", "users bootstrap", "scheduler status"} {
		args := strings.Fields(path)
		if !reflect.DeepEqual(seen["ddp "+path], discovery(t, append([]string{"help"}, args...)...)) ||
			!reflect.DeepEqual(seen["ddp "+path], discovery(t, append(args, "--help")...)) {
			t.Errorf("targeted help disagrees for %s", path)
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil || bytes.Contains(encoded, []byte("never-serialize")) || bytes.Contains(encoded, []byte("does-not-exist")) {
		t.Fatal("discovery serialized runtime values", err)
	}
	if seen["ddp migrate up"].Risk != "write" || seen["ddp migrate status"].Risk != "read" || !strings.Contains(seen["ddp comms resend"].Confirmation, "--confirm") || !strings.Contains(seen["ddp jobs backfill"].Confirmation, "--confirm-executions") {
		t.Fatal("mutation requirements lost")
	}
	if seen["ddp check"].Risk != "external" || seen["ddp check"].RequiredRole != "developer" || seen["ddp config diff"].Risk != "read" || seen["ddp config diff"].RequiredRole != "local_reader" {
		t.Fatal("development command authority lost")
	}
}

func TestCatalogRejectsUndeclaredCommandsAndPreservesFlagState(t *testing.T) {
	catalog := commandCatalog{}
	root := catalog.declare(localRead, &cobra.Command{Use: "ddp"})
	root.PersistentFlags().String("config", "ddp.yaml", "registry")
	child := catalog.declare(localRead, &cobra.Command{Use: "read", Aliases: []string{"peek"}, RunE: func(*cobra.Command, []string) error { return nil }})
	child.Flags().String("filter", "", "filter")
	root.AddCommand(child)
	before := child.InheritedFlags().NFlag()
	first := catalog.describe(child)
	second := catalog.describe(child)
	if !reflect.DeepEqual(first, second) || before != child.InheritedFlags().NFlag() || child.InheritedFlags().Lookup("filter") != nil {
		t.Fatal("description mutated inherited flags")
	}
	resolved, remaining, err := root.Find([]string{"peek"})
	if err != nil || len(remaining) != 0 || catalog.describe(resolved).Name != "ddp read" || !slices.Equal(first.Aliases, []string{"peek"}) {
		t.Fatal("alias did not resolve to canonical declaration")
	}
	root.AddCommand(&cobra.Command{Use: "unsafe", RunE: func(*cobra.Command, []string) error { t.Fatal("undeclared handler ran"); return nil }})
	if err := catalog.validate(root); err == nil {
		t.Fatal("undeclared command accepted")
	}
	var machine bool
	catalog.install(root, &machine, func(any) error { return nil })
	root.SetArgs([]string{"unsafe"})
	root.SilenceErrors, root.SilenceUsage = true, true
	if err := root.ExecuteContext(t.Context()); err == nil {
		t.Fatal("runtime admitted undeclared command")
	}
}

func TestDiscoveryRejectsUnknownHelpAndExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"help", "jobs", "nonexistent"}, {"capabilities", "unexpected"}} {
		var out, errOut bytes.Buffer
		if code := Execute(t.Context(), append(args, "--json"), &out, &errOut, nil); code != 2 || !json.Valid(out.Bytes()) || errOut.Len() != 0 {
			t.Fatalf("%v: %d %s %s", args, code, &out, &errOut)
		}
	}
}
