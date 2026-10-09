package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/audit"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/comms"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/spf13/cobra"
)

func commsCommand(catalog commandCatalog, registry *string, write func(any) error, noArgs, oneRef cobra.PositionalArgs) *cobra.Command {
	command := catalog.declare(localRead, &cobra.Command{Use: "comms", Short: "Inspect and deliver persisted email"})
	command.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "list", Short: "List the newest 100 message records without bodies", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		rows, err := comms.List(cmd.Context(), pool)
		if err != nil {
			return err
		}
		return write(rows)
	}}))
	command.AddCommand(catalog.declare(databaseRead, &cobra.Command{Use: "show message/<id>", Short: "Inspect message state and delivery attempts without bodies", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		detail, err := comms.Show(cmd.Context(), pool, args[0])
		if err != nil {
			return err
		}
		return write(detail)
	}}))
	var dataPath string
	preview := catalog.declare(localRead, &cobra.Command{Use: "preview <template>", Short: "Render a template locally without enqueueing or sending", Args: oneRef, RunE: func(_ *cobra.Command, args []string) error {
		var values map[string]any
		if err := readMessageJSON(dataPath, &values); err != nil {
			return err
		}
		result, err := comms.Render(filepath.Dir(*registry), args[0], values)
		if err != nil {
			return err
		}
		return write(result)
	}})
	preview.Flags().StringVar(&dataPath, "data", "", "JSON context file")
	command.AddCommand(preview)
	for _, name := range []string{"enqueue", "test-send"} {
		var inputPath string
		queue := catalog.declare(commandPolicy{"external", "operator", "none", "transactional: comms.enqueue or comms.test-send; delivery attempts persisted separately"}, &cobra.Command{Use: name, Short: "Persist an email from an explicit JSON input file", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			var input comms.Input
			if err := readMessageJSON(inputPath, &input); err != nil {
				return err
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
			tx, err := pool.Begin(cmd.Context())
			if err != nil {
				return errors.New("open communications transaction")
			}
			defer tx.Rollback(cmd.Context())
			id, err := comms.Enqueue(cmd.Context(), tx, cfg.Comms, input)
			if err != nil {
				return err
			}
			if err = audit.Record(cmd.Context(), tx, "comms."+name, "message/"+id, map[string]string{"template": input.Template}); err != nil {
				return err
			}
			if err = tx.Commit(cmd.Context()); err != nil {
				return errors.New("commit communications transaction")
			}
			if name == "test-send" {
				result, err := comms.RelayOne(cmd.Context(), pool, cfg.Comms, filepath.Dir(*registry), "message/"+id)
				if err != nil {
					return err
				}
				return write(result)
			}
			return write(map[string]string{"id": id})
		}})
		if name == "test-send" {
			queue.Short = "Persist a test email and attempt only that message's delivery"
		}
		queue.Flags().StringVar(&inputPath, "file", "", "JSON with effect_key, template, recipients and context")
		command.AddCommand(queue)
	}
	command.AddCommand(catalog.declare(commandPolicy{"write", "operator", "none", "transactional: ddp.audit"}, &cobra.Command{Use: "retry message/<id>", Short: "Retry a failed message using its saved content", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		if err = comms.Retry(cmd.Context(), pool, args[0]); err != nil {
			return err
		}
		return write(map[string]string{"id": args[0], "status": "pending"})
	}}))
	var effect string
	var confirm bool
	resend := catalog.declare(commandPolicy{"external", "operator", "--confirm and a new --effect-key", "transactional: comms.resend"}, &cobra.Command{Use: "resend message/<id>", Short: "Create a new effect from a delivered message; requires --confirm", Args: oneRef, RunE: func(cmd *cobra.Command, args []string) error {
		if !confirm {
			return refusedError{errors.New("resend requires --confirm and a new --effect-key")}
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		id, err := comms.Resend(cmd.Context(), pool, args[0], effect)
		if err != nil {
			return err
		}
		return write(map[string]string{"id": id})
	}})
	resend.Flags().StringVar(&effect, "effect-key", "", "New stable resend effect key")
	resend.Flags().BoolVar(&confirm, "confirm", false, "Confirm another delivery of the saved message")
	command.AddCommand(resend)
	var once bool
	relay := catalog.declare(commandPolicy{"external", "operator", "none", "operational: delivery attempts and outbox state"}, &cobra.Command{Use: "relay", Short: "Deliver due messages through SMTP; --once attempts at most one", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(*registry)
		if err != nil {
			return err
		}
		pool, err := openPool(cmd.Context())
		if err != nil {
			return err
		}
		defer pool.Close()
		for {
			result, err := comms.RelayOne(cmd.Context(), pool, cfg.Comms, filepath.Dir(*registry), "")
			if err != nil {
				return err
			}
			if once {
				return write(result)
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-cmd.Context().Done():
				timer.Stop()
				return write(map[string]string{"status": "stopped"})
			case <-timer.C:
			}
		}
	}})
	relay.Flags().BoolVar(&once, "once", false, "Attempt one due message, then return its outcome")
	command.AddCommand(relay)
	return command
}

func readMessageJSON(path string, dst any) error {
	if path == "" {
		return usageError{errors.New("a JSON input file is required")}
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("read message JSON file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (256<<10)+1))
	if err != nil || len(data) > 256<<10 {
		return errors.New("message JSON exceeds 256 KiB or could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dst); err != nil {
		return errors.New("invalid message JSON")
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("one JSON value required")
	}
	return nil
}
