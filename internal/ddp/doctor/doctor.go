// Package doctor gathers live operational checks without changing the system.
package doctor

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/comms"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Check struct {
	Ref string `json:"ref"`
	platform.Result
	Repair string `json:"repair,omitempty"`
}

type Report struct {
	ObservedAt time.Time `json:"observed_at"`
	State      string    `json:"state"`
	Checks     []Check   `json:"checks"`
}

// Run reports unavailable checks as unknown and never interprets missing evidence as healthy.
func Run(ctx context.Context, registry string) Report {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r := Report{ObservedAt: time.Now().UTC(), State: "ok", Checks: []Check{}}
	root := filepath.Dir(registry)
	add := func(ref string, result platform.Result, repair string) {
		if result.State == "ok" {
			repair = ""
		}
		r.Checks = append(r.Checks, Check{Ref: ref, Result: result, Repair: repair})
		if result.State == "failing" {
			r.State = "failing"
		} else if result.State != "ok" && r.State == "ok" {
			r.State = "unknown"
		}
	}
	cfg, err := config.Load(registry)
	if err != nil {
		add("ddp:config", result("failing", "Project configuration is invalid or unavailable."), "Run ddp config validate locally and correct the registry and referenced files.")
	} else {
		add("ddp:config", result("ok", "Project configuration and referenced files are valid."), "")
	}
	add("ddp:env_file", EnvFile(root), "Create a regular .env file owned by the operator and run chmod 600 .env.")
	add("ddp:docker", Docker(ctx), "Start Docker and run doctor as the authorized host operator with access to its daemon.")
	if cfg != nil {
		add("ddp:secrets", Secrets(cfg), "Supply the named variables in the process environment from the protected .env file; restart affected services.")
		add("ddp:template", Template(ctx, root, cfg.Ddp.TemplateRevision), "Inspect the recorded template revision and platform changes in Git; fetch the recorded revision if absent and review any template merge.")
		add("ddp:port", Port(ctx, cfg.Serving.Addr), "Inspect the configured serving address and occupying service; restore API readiness or free the port before starting DDP.")
		if cfg.Comms.SMTP == nil {
			add("ddp:smtp", result("ok", "SMTP is not configured; connectivity does not apply."), "")
		} else if err := comms.Probe(ctx, *cfg.Comms.SMTP); err != nil {
			add("ddp:smtp", result("unknown", "SMTP greeting or TLS negotiation could not be verified."), "Check SMTP address, network access and TLS certificates. This probe does not test authentication or delivery.")
		} else {
			add("ddp:smtp", result("ok", "SMTP greeting and configured TLS negotiation succeeded; no authentication or mail was sent."), "")
		}
	}
	var pool *pgxpool.Pool
	if raw := os.Getenv("DATABASE_URL"); raw != "" {
		pool, _ = pgxpool.New(ctx, raw)
		if pool != nil {
			defer pool.Close()
		}
	}
	for _, observation := range health.Platform(ctx, pool, root, cfg) {
		add(observation.Ref, platform.Result{State: observation.State, Severity: observation.Severity, Message: observation.Message, Value: observation.Value}, platformRepair(observation.Ref))
	}
	add("ddp:migrations", Migrations(ctx, pool), "Inspect ddp migrate status with the deployed binary; apply pending migrations through the reviewed deployment procedure. Never edit an applied migration.")
	add("ddp:clock", Clock(ctx, pool), "Restore database connectivity and synchronize the host and database clocks with the host time service.")
	return r
}

func result(state, message string) platform.Result {
	return platform.Result{State: state, Severity: "critical", Message: message}
}

func platformRepair(ref string) string {
	switch ref {
	case "ddp:database":
		return "Check DATABASE_URL and the Postgres service; restore connectivity before retrying."
	case "ddp:scheduler":
		return "Inspect ddp scheduler status and scheduler service logs; start or recover the scheduler."
	case "ddp:deployment":
		return "Inspect ddp deploy status --json and the host release journal; retain the failed image digest before retrying deployment."
	case "ddp:disk":
		return "Inspect space on the project filesystem and safely free capacity or expand the volume."
	case "ddp:outbox":
		return "Inspect ddp comms list and relay logs; restore SMTP connectivity and retry failed retained messages."
	case "ddp:backup":
		return "Inspect backup maintenance logs; run a verified off-host backup and a disposable restore test with the recorded image."
	default:
		return "Inspect the corresponding health check and service logs."
	}
}

func EnvFile(root string) platform.Result {
	info, err := os.Lstat(filepath.Join(root, ".env"))
	if err != nil {
		return result("failing", "The project .env file is missing or cannot be inspected.")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return result("failing", "The project .env must be a regular file with mode 0600; symlinks are not accepted.")
	}
	return result("ok", "The project .env is a regular file with mode 0600; its contents were not read.")
}

func Secrets(cfg *config.Config) platform.Result {
	names := []string{"DATABASE_URL"}
	if cfg.Deploy.Backup != nil {
		names = append(names, "BACKUP_DATABASE_URL", "BACKUP_ACCESS_KEY_ID", "BACKUP_SECRET_ACCESS_KEY", "BACKUP_AGE_IDENTITY")
	}
	for _, integration := range cfg.Integrations {
		if integration.Auth != nil {
			names = append(names, integration.Auth.Secret)
		}
	}
	if cfg.Comms.SMTP != nil && cfg.Comms.SMTP.PasswordEnv != "" {
		names = append(names, cfg.Comms.SMTP.PasswordEnv)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	missing := []string{}
	for _, name := range names {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return platform.Result{State: "failing", Severity: "critical", Message: "Required environment variables are missing.", Value: strings.Join(missing, ", ")}
	}
	return result("ok", "All declared secret names are present in the process environment; values were not reported.")
}
