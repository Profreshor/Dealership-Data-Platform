package doctor

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/platform"
)

var revisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

// These provenance paths come from ARCHITECTURE.md §3; client changes are allowed
// but must be reviewed when merging a template update.
var platformPaths = []string{"cmd/ddp", "internal/ddp", "ddp", "migrations/ddp", "frontend/packages/ddp-ui", "deploy", "bin", "AGENTS.md", "CLAUDE.md", "skills", ".agents/skills", ".claude/skills"}

func Template(ctx context.Context, root, revision string) platform.Result {
	unknown := result("unknown", "The recorded template revision could not be compared with local platform files.")
	if !revisionPattern.MatchString(revision) {
		return unknown
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "git", "-C", root, "cat-file", "-e", revision+"^{commit}").Run(); err != nil {
		return unknown
	}
	args := append([]string{"-C", root, "diff", "--quiet", "--no-ext-diff", "--no-textconv", revision, "--"}, platformPaths...)
	err := exec.CommandContext(ctx, "git", args...).Run()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return result("failing", "Platform files differ from the recorded template revision; review intentional changes before upgrading.")
		}
		return unknown
	}
	args = append([]string{"-C", root, "ls-files", "--others", "--exclude-standard", "--"}, platformPaths...)
	untracked, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return unknown
	}
	if len(untracked) > 0 {
		return result("failing", "Untracked platform files are present; review and commit or remove them before upgrading.")
	}
	return result("ok", "Local platform files match the recorded template revision; no remote update was fetched.")
}
