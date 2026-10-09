package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type checkResult struct {
	Mode    string   `json:"mode"`
	Files   []string `json:"files"`
	Targets []string `json:"targets"`
	Passed  bool     `json:"passed"`
}

func checkCommand(catalog commandCatalog, registry *string, write func(any) error, logs io.Writer, noArgs cobra.PositionalArgs) *cobra.Command {
	var changed bool
	command := catalog.declare(commandPolicy{"external", "developer", "none", "none: development checks"}, &cobra.Command{
		Use: "check", Short: "Run development checks", Args: noArgs,
		Long: "Run make check, check-smoke and check-image serially in the source checkout. Requires installed toolchains, Playwright Chromium, Docker and disposable development Postgres. --changed selects affected targets from staged, unstaged and nonignored untracked files at the Git checkout root; it does not replace the full pre-PR gate. --config must name the checkout's ddp.yaml. Child logs go to stderr and the final result to stdout. Stops on the first failed target.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := checkRoot(*registry)
			if err != nil {
				return err
			}
			result := checkResult{Mode: "all", Files: []string{}, Targets: []string{"check", "check-smoke", "check-image"}, Passed: true}
			if changed {
				result.Mode = "changed"
				result.Files, err = changedFiles(cmd.Context(), root)
				if err != nil {
					return err
				}
				result.Targets = checkTargets(result.Files)
			}
			for _, target := range result.Targets {
				if err := runMake(cmd.Context(), root, target, logs); err != nil {
					return fmt.Errorf("check target %s failed: %w", target, err)
				}
			}
			if result.Mode == "changed" {
				if err := runGitCheck(cmd.Context(), root, logs); err != nil {
					return fmt.Errorf("check target git diff --check failed: %w", err)
				}
			}
			return write(result)
		},
	})
	command.Flags().BoolVar(&changed, "changed", false, "Check targets affected by changed files")
	return command
}

func checkRoot(configPath string) (string, error) {
	root := filepath.Dir(configPath)
	if filepath.Base(configPath) != "ddp.yaml" {
		return "", usageError{errors.New("--config must name the canonical ddp.yaml registry")}
	}
	if info, err := os.Stat(configPath); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("configured ddp.yaml registry is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(filepath.Join(root, "Makefile")); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("project Makefile is required")
	}
	return root, nil
}

func changedFiles(ctx context.Context, root string) ([]string, error) {
	canonicalTop, err := gitRoot(ctx, root)
	absRoot, absErr := filepath.Abs(root)
	canonicalRoot, rootErr := filepath.EvalSymlinks(absRoot)
	if err != nil || absErr != nil || rootErr != nil || canonicalTop != canonicalRoot {
		return nil, errors.New("check --changed must run at the Git checkout root with HEAD")
	}
	tracked, err := gitNUL(ctx, root, "diff", "--name-only", "--no-renames", "HEAD")
	if err != nil {
		return nil, errors.New("check --changed requires a Git checkout with HEAD")
	}
	// Include staged changes even when an unstaged edit restores the HEAD content.
	staged, err := gitNUL(ctx, root, "diff", "--cached", "--name-only", "--no-renames", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read staged changes: %w", err)
	}
	untracked, err := gitNUL(ctx, root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, errors.New("check --changed requires a Git checkout")
	}
	files := append(append(tracked, staged...), untracked...)
	slices.Sort(files)
	return slices.Compact(files), nil
}

func gitNUL(ctx context.Context, root string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, append(args, "-z")...)...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(out), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts, nil
}

// ponytail: run whole affected stages; narrow to packages if local feedback becomes too slow.
func checkTargets(files []string) []string {
	if len(files) == 0 {
		return []string{}
	}
	var goCheck, pythonCheck, frontendCheck, docsOnly = false, false, false, true
	for _, file := range files {
		lower := strings.ToLower(file)
		ext := strings.ToLower(filepath.Ext(file))
		switch {
		case lower == "makefile" || strings.HasPrefix(lower, ".github/") || strings.HasPrefix(lower, ".devcontainer/") || strings.HasPrefix(lower, "deploy/") || strings.HasPrefix(lower, "tests/proving-ground/"):
			return []string{"check", "check-smoke", "check-image"}
		case ext == ".md" || ext == ".markdown":
		case ext == ".go" || lower == "go.mod" || lower == "go.sum" || strings.HasPrefix(lower, "cmd/") || strings.HasPrefix(lower, "internal/") || strings.HasPrefix(lower, "migrations/"):
			goCheck, docsOnly = true, false
		case ext == ".py" || lower == "pyproject.toml" || lower == "uv.lock" || strings.HasPrefix(lower, "ddp/") || strings.HasPrefix(lower, "client/") || strings.HasPrefix(lower, "jobs/") || strings.HasPrefix(lower, "tests/python/"):
			pythonCheck, docsOnly = true, false
		case strings.HasPrefix(lower, "frontend/"):
			frontendCheck, docsOnly = true, false
		case ext == ".sql" || strings.HasPrefix(lower, "models/") || strings.HasPrefix(lower, "health/") || lower == "ddp.yaml" || strings.HasPrefix(lower, "schema/") || strings.HasPrefix(lower, "templates/"):
			goCheck, frontendCheck, docsOnly = true, true, false
		default:
			return []string{"check", "check-smoke", "check-image"}
		}
	}
	if docsOnly {
		return []string{"secrets"}
	}
	targets := []string{}
	if goCheck {
		targets = append(targets, "check-go")
	}
	if pythonCheck {
		targets = append(targets, "check-python")
	}
	if frontendCheck {
		targets = append(targets, "check-frontend")
	}
	targets = append(targets, "check-system")
	return slices.Compact(targets)
}

func runMake(ctx context.Context, root, target string, logs io.Writer) error {
	cmd := exec.CommandContext(ctx, "make", "-j1", target)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, logs, logs
	cmd.Env = makeEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return err
}

func makeEnv() []string {
	result := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "MAKEFLAGS=") || strings.HasPrefix(value, "GNUMAKEFLAGS=") || strings.HasPrefix(value, "MFLAGS=") || strings.HasPrefix(value, "MAKELEVEL=") {
			continue
		}
		result = append(result, value)
	}
	return result
}

func runGitCheck(ctx context.Context, root string, logs io.Writer) error {
	for _, args := range [][]string{{"diff", "--check", "HEAD"}, {"diff", "--cached", "--check", "HEAD"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		cmd.Stdout, cmd.Stderr = logs, logs
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}
