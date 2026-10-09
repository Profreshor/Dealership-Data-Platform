// Package dev starts the local development services without production credentials.
package dev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/migrate"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/service"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/serving"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
	"github.com/jackc/pgx/v5"
)

const DatabaseURL = "postgres://postgres:ddp-local-only@127.0.0.1:55432/ddp_development?sslmode=disable"

type Options struct {
	Scheduler            bool
	Comms                bool
	APIMetricsAddr       string
	SchedulerMetricsAddr string
}

func Run(ctx context.Context, c *config.Config, root string, logs io.Writer, register func(*web.Registry), options Options) error {
	if c == nil {
		return errors.New("development config is required")
	}
	if options.Comms && !options.Scheduler {
		return fmt.Errorf("development comms requires scheduler")
	}
	urlValue := os.Getenv("DDP_DEV_DATABASE_URL")
	if urlValue == "" {
		urlValue = DatabaseURL
	}
	if err := validateURL(urlValue); err != nil {
		return err
	}
	if os.Getenv("DDP_DEV_DATABASE_URL") == "" {
		cmd := exec.CommandContext(ctx, "docker", "compose", "-p", "ddp-dev", "-f", filepath.Join(root, "deploy/compose.dev.yaml"), "up", "-d", "--wait", "postgres")
		cmd.Stdout = logs
		cmd.Stderr = logs
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("start development Postgres: %w", err)
		}
	}
	conn, err := pgx.Connect(ctx, urlValue)
	if err != nil {
		return errors.New("connect to development Postgres failed")
	}
	err = migrate.Up(ctx, conn)
	_ = conn.Close(ctx)
	if err != nil {
		return err
	}
	apiURL, err := roleURL(urlValue, "ddp_api")
	if err != nil {
		return err
	}
	jobURL, err := roleURL(urlValue, "ddp_job")
	if err != nil {
		return err
	}
	schedulerURL, err := roleURL(urlValue, "ddp_scheduler")
	if err != nil {
		return err
	}
	local := *c
	if !options.Comms {
		local.Comms.SMTP = nil
	}
	previousJobURL, hadJobURL := os.LookupEnv("JOB_DATABASE_URL")
	if err := os.Setenv("JOB_DATABASE_URL", jobURL); err != nil {
		return errors.New("set development job database")
	}
	defer func() {
		if hadJobURL {
			_ = os.Setenv("JOB_DATABASE_URL", previousJobURL)
		} else {
			_ = os.Unsetenv("JOB_DATABASE_URL")
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	vite := exec.CommandContext(ctx, "npm", "run", "dev", "--workspace", "apps/portal", "--", "--host", "0.0.0.0", "--strictPort")
	vite.Dir = filepath.Join(root, "frontend")
	vite.Stdout = logs
	vite.Stderr = logs
	vite.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	vite.Cancel = func() error {
		err := syscall.Kill(-vite.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	vite.WaitDelay = 5 * time.Second
	if err := vite.Start(); err != nil {
		return fmt.Errorf("start Vite: %w", err)
	}
	done := make(chan error, 2)
	go func() {
		err := vite.Wait()
		// WaitDelay kills the parent; also reap any descendants that ignored TERM.
		_ = syscall.Kill(-vite.Process.Pid, syscall.SIGKILL)
		if ctx.Err() != nil {
			var exitErr *exec.ExitError
			expectedSignal := false
			if errors.As(err, &exitErr) {
				signal := exitErr.Sys().(syscall.WaitStatus).Signal()
				expectedSignal = signal == syscall.SIGTERM || signal == syscall.SIGKILL
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, exec.ErrWaitDelay) || expectedSignal {
				err = nil
			}
		} else if err == nil {
			err = errors.New("Vite stopped unexpectedly")
		}
		done <- err
	}()

	go func() {
		if options.Scheduler {
			done <- service.RunAll(ctx, &local, root, service.Options{APIURL: apiURL, SchedulerURL: schedulerURL, APIMetricsAddr: options.APIMetricsAddr, SchedulerMetricsAddr: options.SchedulerMetricsAddr}, logs, register)
			return
		}
		done <- serving.Run(ctx, &local, apiURL, options.APIMetricsAddr, register)
	}()
	err = <-done
	cancel()
	return errors.Join(err, <-done)
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || strings.Contains(raw, " ") {
		return fmt.Errorf("DDP_DEV_DATABASE_URL must be a PostgreSQL URL")
	}
	return nil
}

func roleURL(raw, role string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("development database URL is invalid")
	}
	q := u.Query()
	q.Set("role", role)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
