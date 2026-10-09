package onboard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestInitializeCreatesOnlyFreshLocalClientFiles(t *testing.T) {
	source := t.TempDir()
	base, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]string{"AGENTS.md": "Keep this guide verbatim.\n", "go.mod": "module test\n", "Makefile": "check:\n\ttrue\n", "ddp.yaml": string(base), "deploy/Dockerfile": "FROM scratch\n", "migrations/embed.go": "package migrations\n", "tests/proving-ground/private.txt": "omit deployment", ".env": "omit credentials", "client/real.py": "\"\"\"template code\"\"\"\n", "skills/ddp-onboard/SKILL.md": "canonical procedure\n"}
	for name, data := range original {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, vendor := range []string{".agents", ".claude"} {
		if err := os.Mkdir(filepath.Join(source, vendor), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../skills", filepath.Join(source, vendor, "skills")); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", source}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-qm", "template")
	if err := os.WriteFile(filepath.Join(source, "client/untracked.py"), []byte("do not copy"), 0600); err != nil {
		t.Fatal(err)
	}
	discovery := strings.Replace(validDiscovery, "systems: []", `systems:
  - name: dms
    kind: dms
    vendor: Synthetic DMS
    access: api
    docs_url: https://example.test/docs
    credential_owner: dealership
    entities: [{name: customers, deletion_behavior: ignore}]`, 1)
	discovery = strings.Replace(discovery, "groups: []", "groups: [{name: platform_ops, recipients: [operator@example.test]}]", 1)
	facts := filepath.Join(t.TempDir(), "discovery.yaml")
	if err := os.WriteFile(facts, []byte(discovery), 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{Template: source, Directory: filepath.Join(t.TempDir(), "client"), Discovery: facts, DryRun: true}
	report, err := Initialize(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || len(report.Files) == 0 {
		t.Fatal(report)
	}
	if _, err := os.Stat(options.Directory); !os.IsNotExist(err) {
		t.Fatalf("dry run created directory: %v", err)
	}
	options.DryRun = false
	if _, err := Initialize(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(options.Directory, "ddp.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ddp.Name != "acme" || cfg.Serving.PublicURL != "https://app.example.com" || cfg.Deploy.Image != "ghcr.io/acme/portal" || cfg.Integrations["dms"].Settings["access"] != "api" || len(cfg.Jobs) != 0 {
		t.Fatalf("invented or lost discovery facts: %#v", cfg)
	}
	for _, path := range []string{"tests/proving-ground", ".env", ".git", "client/untracked.py"} {
		if _, err := os.Stat(filepath.Join(options.Directory, path)); !os.IsNotExist(err) {
			t.Fatalf("copied excluded %s", path)
		}
	}
	for _, root := range []string{source, options.Directory} {
		got, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
		if err != nil || string(got) != original["AGENTS.md"] {
			t.Fatalf("changed authoritative guide: %s %v", got, err)
		}
	}
	for _, vendor := range []string{".agents", ".claude"} {
		link := filepath.Join(options.Directory, vendor, "skills")
		if target, err := os.Readlink(link); err != nil || target != "../skills" {
			t.Fatalf("missing canonical skill link: %s %v", target, err)
		}
		if data, err := os.ReadFile(filepath.Join(link, "ddp-onboard/SKILL.md")); err != nil || string(data) != "canonical procedure\n" {
			t.Fatalf("skill discovery failed: %s %v", data, err)
		}
	}
	if _, err := Initialize(t.Context(), options); err == nil {
		t.Fatal("overwrote existing directory")
	}
	options.Directory = filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(source, options.Directory); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(t.Context(), options); err == nil {
		t.Fatal("accepted destination symlink")
	}
	options.Directory = filepath.Join(t.TempDir(), "cancelled")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Initialize(ctx, options); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := os.Stat(options.Directory); !os.IsNotExist(err) {
		t.Fatal("cancelled initialization wrote output")
	}
	longName := strings.Repeat("a", 63)
	if err := os.WriteFile(facts, []byte(strings.Replace(discovery, "name: dms", "name: "+longName, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	module, err := os.ReadFile(filepath.Join(options.Directory, "client", longName+".py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(module), "\n") {
		if len(line) > 100 {
			t.Fatal("valid system identifier produced a module that fails Ruff's line limit")
		}
	}
	options.Directory = filepath.Join(t.TempDir(), "conflicting")
	if err := os.WriteFile(facts, []byte(strings.Replace(discovery, "name: dms", "name: real", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(t.Context(), options); err == nil || !strings.Contains(err.Error(), "conflicts with template source") {
		t.Fatalf("overwrote template integration: %v", err)
	}
	if _, err := os.Stat(options.Directory); !os.IsNotExist(err) {
		t.Fatal("conflicting integration wrote output")
	}
	if err := os.WriteFile(filepath.Join(source, "client/real.py"), []byte("changed source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(t.Context(), options); err == nil || !strings.Contains(err.Error(), "uncommitted tracked changes") {
		t.Fatalf("accepted misleading template revision: %v", err)
	}
	if _, err := os.Stat(options.Directory); !os.IsNotExist(err) {
		t.Fatal("dirty template wrote output")
	}
	link := filepath.Join(source, ".agents", "skills")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", link); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "unsafe skill link")
	if _, err := Initialize(t.Context(), options); err == nil || !strings.Contains(err.Error(), "skill discovery link") {
		t.Fatalf("accepted escaping skill link: %v", err)
	}
	if _, err := os.Stat(options.Directory); !os.IsNotExist(err) {
		t.Fatal("unsafe skill link wrote output")
	}
}

func TestDiscoveryRejectsUnsafeURLsAndIncompleteLists(t *testing.T) {
	for _, change := range [][2]string{{"app.example.com", "app.example.com:443"}, {"app.example.com", "app/example.com"}, {"https://github.com/acme/portal", "https://user:secret@github.com/acme/portal"}, {"systems: []", "systems: null"}, {"backup_retention: 14d", "backup_retention: 9223372036854775807d"}, {"backup_retention: 14d", "backup_retention: 14junkd"}} {
		if _, err := ParseDiscovery([]byte(strings.Replace(validDiscovery, change[0], change[1], 1))); err == nil {
			t.Fatalf("accepted %s", change[0])
		}
	}
}
