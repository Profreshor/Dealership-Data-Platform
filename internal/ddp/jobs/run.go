// Package jobs runs bounded Python and SQL model jobs with durable attempts.
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/backup"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/buildinfo"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/cleanup"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/comms"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/health"
	modelpkg "github.com/Profreshor/Dealership-Data-Platform/internal/ddp/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Execution struct {
	ID        string `json:"execution_id"`
	AttemptID string `json:"attempt_id"`
	Job       string `json:"job"`
	Status    string `json:"status"`
}

type Result struct {
	RowsRead    *int64          `json:"rows_read,omitempty"`
	RowsWritten *int64          `json:"rows_written,omitempty"`
	Watermark   json.RawMessage `json:"watermark,omitempty"`
	Warnings    []string        `json:"warnings,omitempty"`
	Details     map[string]any  `json:"details,omitempty"`
}

type runContext struct {
	ExecutionID  string                        `json:"execution_id"`
	AttemptID    string                        `json:"attempt_id"`
	ScheduledAt  time.Time                     `json:"scheduled_at"`
	Timezone     string                        `json:"timezone"`
	Reads        []string                      `json:"reads"`
	Writes       []config.Write                `json:"writes"`
	Settings     map[string]any                `json:"settings"`
	Integrations map[string]config.Integration `json:"integrations"`
	Watermarks   map[string]json.RawMessage    `json:"watermarks"`
}

func executeAttempt(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, root string, run Execution, scheduledAt time.Time) (Result, string, string, error) {
	job, duration, err := jobDefinition(cfg, run.Job)
	if err != nil {
		return Result{}, "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	ref := run.Job
	if ref == BackupRef {
		raw := os.Getenv("BACKUP_DATABASE_URL")
		if raw == "" {
			return Result{}, "", "", errors.New("BACKUP_DATABASE_URL is required for the backup role")
		}
		result, err := backup.Run(ctx, cfg, raw, backup.RunOptions{Revision: buildinfo.Version, Image: os.Getenv("DDP_IMAGE_DIGEST"), IfDue: true})
		return Result{Details: map[string]any{"backup": result}}, "", "", err
	}
	if ref == CommsRelayRef {
		result, err := relayMessages(ctx, pool, cfg.Comms, root)
		return result, "", "", err
	}
	if ref == HealthRef {
		evaluations, err := health.Evaluate(ctx, pool, cfg, root)
		if err != nil {
			return Result{}, "", "", err
		}
		states := map[string]int{"ok": 0, "failing": 0, "unknown": 0}
		for _, evaluation := range evaluations {
			if _, ok := states[evaluation.State]; ok {
				states[evaluation.State]++
			}
		}
		return Result{Details: map[string]any{"state_counts": states}}, "", "", nil
	}
	if ref == CleanupRef {
		counts, err := cleanup.Run(ctx, pool, cfg)
		return Result{Details: map[string]any{"expired": counts}}, "", "", err
	}
	var python string
	var env, secrets []string
	integrations := map[string]config.Integration{}
	if job.Python != "" {
		python = filepath.Join(root, ".venv/bin/python")
		if _, err := os.Stat(python); err != nil {
			python = "python3"
		} else {
			python, err = filepath.Abs(python)
			if err != nil {
				return Result{}, "", "", err
			}
		}
		jobURL := os.Getenv("JOB_DATABASE_URL")
		if jobURL == "" {
			return Result{}, "", "", fmt.Errorf("JOB_DATABASE_URL is required for the Python job role")
		}
		env = []string{"PATH=" + os.Getenv("PATH"), "DATABASE_URL=" + jobURL, "PYTHONUNBUFFERED=1", "PYTHONDONTWRITEBYTECODE=1", "LANG=C.UTF-8"}
		secrets = []string{jobURL}
		for _, read := range job.Reads {
			name, ok := strings.CutPrefix(read, "integration/")
			if !ok {
				continue
			}
			integration, ok := cfg.Integrations[name]
			if !ok {
				return Result{}, "", "", fmt.Errorf("unknown integration %q", name)
			}
			integrations[name] = integration
			if integration.Auth != nil {
				secret := os.Getenv(integration.Auth.Secret)
				if secret == "" {
					return Result{}, "", "", fmt.Errorf("required integration secret %s is missing", integration.Auth.Secret)
				}
				env = append(env, integration.Auth.Secret+"="+secret)
				secrets = append(secrets, secret)
			}
		}
	}
	contextData := runContext{ExecutionID: run.ID, AttemptID: run.AttemptID, ScheduledAt: scheduledAt, Timezone: cfg.Ddp.Timezone, Reads: append([]string{}, job.Reads...), Writes: append([]config.Write{}, job.Writes...), Settings: job.Settings, Integrations: integrations, Watermarks: map[string]json.RawMessage{}}
	if contextData.Settings == nil {
		contextData.Settings = map[string]any{}
	}
	var watermark json.RawMessage
	err = pool.QueryRow(ctx, "SELECT value FROM ops.watermarks WHERE job_ref=$1", ref).Scan(&watermark)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, "", "", err
	}
	if len(watermark) > 0 {
		contextData.Watermarks["current"] = watermark
	}
	data, err := json.Marshal(contextData)
	if err != nil {
		return Result{}, "", "", err
	}
	var scratch string
	if job.Python != "" {
		scratch, err = os.MkdirTemp("", "ddp-job-")
		if err != nil {
			return Result{}, "", "", err
		}
		defer os.RemoveAll(scratch)
		if err := os.WriteFile(filepath.Join(scratch, "context.json"), data, 0600); err != nil {
			return Result{}, "", "", err
		}
	}
	var stdout, stderr boundedLog
	var result Result
	var runErr error
	if job.Model != "" {
		modelResult, err := modelpkg.Refresh(ctx, pool, cfg, root, job.Model)
		if err != nil {
			runErr = err
		} else {
			result.Details = map[string]any{"model_ref": modelResult.ModelRef, "status": modelResult.Status}
		}
	} else {
		cmd := exec.Command(python, "-m", "ddp.runner", job.Python, "--context", filepath.Join(scratch, "context.json"), "--result", filepath.Join(scratch, "result.json"))
		cmd.Dir = root
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.WaitDelay = 3 * time.Second
		longest := 0
		for _, secret := range secrets {
			if len(secret) > longest {
				longest = len(secret)
			}
		}
		stdout.limit, stderr.limit = 64*1024+longest, 64*1024+longest
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr = runProcess(ctx, cmd)
		if runErr == nil {
			result, runErr = readResult(filepath.Join(scratch, "result.json"))
		}
	}
	return result, stdout.text(secrets), stderr.text(secrets), runErr
}

func relayMessages(ctx context.Context, pool *pgxpool.Pool, cfg config.Comms, root string) (Result, error) {
	counts := map[string]int{"delivered": 0, "pending": 0, "failed": 0}
	result := Result{Details: map[string]any{"messages": counts}}
	// ponytail: serial batches of at most 100; add bounded workers if measured throughput requires it.
	for n := range 100 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// Reserve a full RelayOne budget before claiming another message.
		if deadline, ok := ctx.Deadline(); ok && n > 0 && time.Until(deadline) < 45*time.Second {
			break
		}
		outcome, err := comms.RelayOne(ctx, pool, cfg, root, "")
		if err != nil {
			return result, err
		}
		if outcome.Status == "idle" || outcome.Status == "busy" {
			break
		}
		counts[outcome.Status]++
	}
	return result, ctx.Err()
}

func runProcess(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Python runner: %w", err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

func readResult(path string) (Result, error) {
	file, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("runner did not write a result")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return Result{}, fmt.Errorf("result exceeds 1 MiB or could not be read")
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return Result{}, fmt.Errorf("result must be a JSON object")
	}
	var result Result
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Result{}, fmt.Errorf("invalid runner result")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Result{}, fmt.Errorf("result contains trailing data")
	}
	for _, count := range []*int64{result.RowsRead, result.RowsWritten} {
		if count != nil && *count < 0 {
			return Result{}, fmt.Errorf("result counts must be nonnegative")
		}
	}
	return result, nil
}

type boundedLog struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedLog) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, err := b.Buffer.Write(p)
	return n, err
}
func (b *boundedLog) text(secrets []string) string {
	text := b.String()
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	if len(text) > 64*1024 {
		text = text[:64*1024]
		b.truncated = true
	}
	if b.truncated {
		text += "\n[output truncated]"
	}
	return strings.ToValidUTF8(strings.ReplaceAll(text, "\x00", "�"), "�")
}
