package jobs

import "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"

const CommsRelayRef = "ddp:comms_relay"
const HealthRef = "ddp:health"
const CleanupRef = "ddp:cleanup"
const BackupRef = "ddp:backup"

// Definitions is the shared declaration of client, model and platform jobs.
func Definitions(cfg *config.Config) map[string]config.Job {
	out := make(map[string]config.Job, len(cfg.Jobs)+len(cfg.Models)+4)
	for name, job := range cfg.Jobs {
		out["job/"+name] = job
	}
	for name, model := range cfg.Models {
		if model.Schedule != "" {
			out["model/"+name] = config.Job{Purpose: model.Purpose, Action: "transform", Model: "model/" + name, Schedule: model.Schedule, Tags: []string{"system"}}
		}
	}
	relay := config.Job{
		Purpose: "Deliver pending email", Action: "notify", Tags: []string{"system"},
		Timeout: "50s", Catchup: "none",
		// Message retries belong to the outbox, independently of scheduler polls.
		Retry:       &config.Retry{MaxAttempts: 1, InitialDelay: "1s", MaxDelay: "1s"},
		Idempotency: &config.Idempotency{Strategy: "duplicates_acceptable", Reason: "SMTP acknowledgement can be lost; retries reuse the saved message identity and content"},
	}
	if cfg.Comms.SMTP != nil {
		relay.Schedule = "* * * * *"
	}
	// Keep history and queued work recognizable when SMTP configuration is removed.
	out[CommsRelayRef] = relay
	out[HealthRef] = config.Job{
		Purpose: "Evaluate health", Action: "check", Schedule: "* * * * *", Timeout: "50s", Catchup: "none",
		Retry: &config.Retry{MaxAttempts: 1, InitialDelay: "1s", MaxDelay: "1s"}, Tags: []string{"system"},
	}
	out[CleanupRef] = config.Job{
		Purpose: "Expire platform evidence and sessions", Action: "maintain", Schedule: "0 * * * *", Timeout: "50s", Catchup: "latest_only",
		Retry: &config.Retry{MaxAttempts: 1, InitialDelay: "1s", MaxDelay: "1s"}, Tags: []string{"system"},
	}
	backup := config.Job{
		Purpose: "Create and verify an encrypted off-host backup", Action: "maintain", Timeout: "2h", Catchup: "latest_only",
		Retry: &config.Retry{MaxAttempts: 1, InitialDelay: "1s", MaxDelay: "1s"}, Tags: []string{"system"},
	}
	if cfg.Deploy.Backup != nil {
		backup.Schedule = "* * * * *" // The backup ledger enforces the declared interval.
	}
	out[BackupRef] = backup
	return out
}

// IsSystem identifies the concrete Go-owned jobs.
func IsSystem(ref string) bool {
	return ref == CommsRelayRef || ref == HealthRef || ref == CleanupRef || ref == BackupRef
}
