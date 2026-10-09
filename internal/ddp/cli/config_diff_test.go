package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
)

func TestDiffJSONSortsPointersAndTreatsArraysAsValues(t *testing.T) {
	changes := diffJSON(map[string]any{"z": 1, "a": map[string]any{"old": true}, "arr": []any{"x"}}, map[string]any{"z": 2, "a": map[string]any{"new": true}, "arr": []any{"y"}, "new/key~": "value"})
	if len(changes) != 5 {
		t.Fatalf("got %d changes: %#v", len(changes), changes)
	}
	want := []string{"/a/new", "/a/old", "/arr", "/new~1key~0", "/z"}
	for i, path := range want {
		if changes[i].Path != path {
			t.Fatalf("change %d path = %q, want %q", i, changes[i].Path, path)
		}
	}
	if changes[0].Kind != "add" || changes[1].Kind != "remove" || changes[2].Kind != "replace" {
		t.Fatalf("unexpected kinds: %#v", changes)
	}
}

func TestNormalizedConfigIgnoresYAMLFormatting(t *testing.T) {
	first, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	second := []byte(strings.Replace(string(first), "  name: ddp\n  display_name: DDP", "  display_name: DDP # same identity\n  name: ddp", 1))
	a, err := normalizedConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := normalizedConfig(second)
	if err != nil {
		t.Fatal(err)
	}
	changes := diffJSON(a, b)
	if len(changes) != 0 {
		t.Fatalf("formatting-only change: %#v", changes)
	}
}

func TestConfigDiffExecuteUsesGitAndOptionalBaseline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "client config.yaml")
	fixture, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", path)
	git("commit", "-qm", "baseline")
	run := func(args ...string) (int, map[string]any) {
		var out bytes.Buffer
		code := Execute(context.Background(), args, &out, &out, func(*web.Registry) {})
		var envelope map[string]any
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatalf("invalid envelope %q: %v", out.String(), err)
		}
		return code, envelope
	}
	if code, report := run("--json", "--config", path, "config", "diff"); code != 0 || report["ok"] != true {
		t.Fatalf("baseline diff: code=%d report=%v", code, report)
	}
	updated := strings.Replace(string(fixture), "display_name: DDP", "display_name: Changed", 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, report := run("--json", "--config", path, "config", "diff"); code != 3 || report["ok"] != true {
		t.Fatalf("unstaged diff: code=%d report=%v", code, report)
	}
	git("add", path)
	if err := os.WriteFile(path, []byte(strings.Replace(updated, "display_name: Changed", "display_name: Final", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, report := run("--json", "--config", path, "config", "diff"); code != 3 || report["ok"] != true {
		t.Fatalf("staged diff: code=%d report=%v", code, report)
	}
	baseline := filepath.Join(t.TempDir(), "baseline.yaml")
	if err := os.WriteFile(baseline, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, report := run("--json", "--config", path, "config", "diff", baseline); code != 3 || report["ok"] != true {
		t.Fatalf("explicit baseline: code=%d report=%v", code, report)
	}
}

func TestConfigDiffErrorsAreSingleJSONEnvelopes(t *testing.T) {
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "config", "diff", "one", "two"}, &out, &out, func(*web.Registry) {})
	if code != 2 || strings.Count(out.String(), `"version":1`) != 1 {
		t.Fatalf("usage output: code=%d body=%s", code, out.String())
	}
}

func TestConfigDiffExplicitBaselineNeedsNoGit(t *testing.T) {
	root := t.TempDir()
	fixture, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "current.yaml")
	baseline := filepath.Join(root, "baseline.yaml")
	if err := os.WriteFile(current, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseline, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := Execute(context.Background(), []string{"--json", "--config", current, "config", "diff", baseline}, &out, &out, func(*web.Registry) {}); code != 0 {
		t.Fatalf("explicit baseline without git: code=%d body=%s", code, out.String())
	}
}

func TestConfigDiffInvalidInputsAndHistoricalEntrypoints(t *testing.T) {
	fixture, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"missing current", "invalid current", "missing baseline", "invalid baseline", "no Git", "no HEAD", "untracked config", "invalid HEAD", "missing entrypoints"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			current, baseline := filepath.Join(root, "ddp.yaml"), filepath.Join(root, "baseline.yaml")
			put := func(path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			git := func(args ...string) {
				t.Helper()
				c := exec.Command("git", append([]string{"-C", root}, args...)...)
				c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("git: %v %s", err, out)
				}
			}
			put(current, fixture)
			put(baseline, fixture)
			args := []string{"config", "diff", baseline, "--config", current, "--json"}
			want := 1
			switch scenario {
			case "missing current":
				if err := os.Remove(current); err != nil {
					t.Fatal(err)
				}
			case "invalid current":
				put(current, []byte("invalid: ["))
			case "missing baseline":
				if err := os.Remove(baseline); err != nil {
					t.Fatal(err)
				}
			case "invalid baseline":
				put(baseline, []byte("invalid: ["))
			case "no Git", "no HEAD", "untracked config", "invalid HEAD":
				args = []string{"config", "diff", "--config", current, "--json"}
				if scenario != "no Git" {
					git("init", "-q")
				}
				if scenario == "untracked config" {
					git("add", "baseline.yaml")
					git("commit", "-qm", "baseline")
				}
				if scenario == "invalid HEAD" {
					put(current, []byte("invalid: ["))
					git("add", "ddp.yaml")
					git("commit", "-qm", "invalid baseline")
					put(current, fixture)
				}
			case "missing entrypoints":
				data := []byte(strings.Replace(string(fixture), "jobs: {}", "jobs: { missing: { purpose: Check rows, action: check, python: jobs.missing } }", 1))
				put(current, data)
				put(baseline, data)
				want = 0
			}
			var out, logs bytes.Buffer
			code := Execute(t.Context(), args, &out, &logs, nil)
			if code != want || !json.Valid(out.Bytes()) || logs.Len() != 0 {
				t.Fatalf("code=%d output=%s logs=%s", code, &out, &logs)
			}
		})
	}
}

func TestConfigDiffPreservesLargeIntegers(t *testing.T) {
	fixture, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	before, err := normalizedConfig([]byte(strings.Replace(string(fixture), "max_workers: 4", "max_workers: 9007199254740992", 1)))
	if err != nil {
		t.Fatal(err)
	}
	after, err := normalizedConfig([]byte(strings.Replace(string(fixture), "max_workers: 4", "max_workers: 9007199254740993", 1)))
	if err != nil {
		t.Fatal(err)
	}
	changes := diffJSON(before, after)
	if len(changes) != 1 || changes[0].Before != json.Number("9007199254740992") || changes[0].After != json.Number("9007199254740993") {
		t.Fatalf("lost integer precision: %+v", changes)
	}
}
