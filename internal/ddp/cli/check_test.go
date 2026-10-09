package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCheckTargets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  []string
	}{
		{"clean", nil, []string{}},
		{"go", []string{"internal/a.go"}, []string{"check-go", "check-system"}},
		{"python and frontend", []string{"jobs/a.py", "frontend/x.tsx"}, []string{"check-python", "check-frontend", "check-system"}},
		{"docs", []string{"docs/notes.md", "README.md"}, []string{"secrets"}},
		{"unknown", []string{"config/local.conf"}, []string{"check", "check-smoke", "check-image"}},
		{"sql", []string{"models/core/a.sql"}, []string{"check-go", "check-frontend", "check-system"}},
		{"full boundaries", []string{"tests/proving-ground/check.py", "deploy/Dockerfile", "Makefile", ".github/workflows/check.yml"}, []string{"check", "check-smoke", "check-image"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkTargets(tc.files); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("checkTargets(%v) = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}

func TestCheckExecuteFullOrderingAndMachineOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MAKEFLAGS", "-n")
	if err := os.WriteFile(filepath.Join(root, "ddp.yaml"), []byte("pages: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	makefile := "check check-smoke check-image:\n\t@echo $@ >> order\n\t@echo stdout-marker\n\t@echo stderr-marker >&2\n"
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte(makefile), 0600); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if code := Execute(t.Context(), []string{"check", "--config", filepath.Join(root, "ddp.yaml"), "--json"}, &out, &logs, nil); code != 0 {
		t.Fatalf("code=%d out=%s logs=%s", code, out.String(), logs.String())
	}
	var envelope struct {
		OK   bool
		Data checkResult
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || !envelope.OK {
		t.Fatalf("envelope=%s err=%v", out.String(), err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "order")); string(got) != "check\ncheck-smoke\ncheck-image\n" {
		t.Fatalf("order=%q", got)
	}
	if strings.Contains(out.String(), "stdout-marker") || !strings.Contains(logs.String(), "stdout-marker") || !strings.Contains(logs.String(), "stderr-marker") {
		t.Fatalf("stdout/logs: %q/%q", out.String(), logs.String())
	}
}

func TestCheckExecuteFailureIsSingleEnvelopeAndStops(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"ddp.yaml": "pages: {}\n", "Makefile": "check:\n\t@echo check >> order\n\t@false\ncheck-smoke:\n\t@echo smoke >> order\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, logs bytes.Buffer
	if code := Execute(t.Context(), []string{"check", "--config", filepath.Join(root, "ddp.yaml"), "--json"}, &out, &logs, nil); code == 0 {
		t.Fatal("failure returned success")
	}
	var envelope struct {
		OK    bool
		Error json.RawMessage
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.OK || len(envelope.Error) == 0 {
		t.Fatalf("error envelope=%s err=%v", out.String(), err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "order")); string(got) != "check\n" {
		t.Fatalf("later target ran: %q", got)
	}
}

func TestRunMakeCancellationKillsDescendants(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "child.pid")
	for name, data := range map[string]string{"Makefile": "wait:\n\t@sh child.sh >/dev/null 2>&1\n", "child.sh": "trap '' TERM\necho $$ > child.pid\nwhile :; do sleep 1; done\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runMake(ctx, root, "wait", io.Discard) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child marker not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid child pid %q: %v", data, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("child was not alive before cancellation: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled make returned success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("make did not cancel")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err != nil || strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("descendant survived cancellation")
}

func TestChangedFilesIncludesUntrackedAndDeleted(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.test")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("tracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("commit", "-qm", "initial")
	if err := os.Remove(filepath.Join(root, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked file.txt"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := changedFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tracked.txt", "untracked file.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changedFiles = %v, want %v", got, want)
	}
}

func TestCheckChangedGitBoundariesAndWhitespace(t *testing.T) {
	root := t.TempDir()
	put := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	put("ddp.yaml", "pages: {}\n")
	put("Makefile", "secrets:\n\t@echo scan\n")
	put("README.md", "baseline\n")
	put(".gitignore", "*.log\n")
	run := func(path string, want int) checkResult {
		t.Helper()
		var out, logs bytes.Buffer
		code := Execute(t.Context(), []string{"check", "--changed", "--config", path, "--json"}, &out, &logs, nil)
		var envelope struct {
			OK   bool
			Data checkResult
		}
		if code != want || json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.OK != (want == 0) {
			t.Fatalf("code=%d out=%s logs=%s", code, &out, &logs)
		}
		return envelope.Data
	}
	path := filepath.Join(root, "ddp.yaml")
	run(path, 1) // No repository.
	git("init", "-q")
	run(path, 1) // No HEAD.
	git("add", ".")
	git("commit", "-qm", "baseline")
	if result := run(path, 0); len(result.Targets) != 0 || result.Files == nil || result.Targets == nil {
		t.Fatalf("clean check: %+v", result)
	}
	put("README.md", "edited\n")
	if result := run(path, 0); !reflect.DeepEqual(result.Targets, []string{"secrets"}) {
		t.Fatalf("docs targets: %+v", result)
	}
	put("README.md", "trailing whitespace \n")
	git("add", "README.md")
	put("README.md", "baseline\n")
	if files, err := changedFiles(t.Context(), root); err != nil || !reflect.DeepEqual(files, []string{"README.md"}) {
		t.Fatalf("lost staged changes: %v %v", files, err)
	}
	run(path, 1) // Staged whitespace must fail even after the working file returns to HEAD.
	put("README.md", "edited\n")
	git("add", "README.md")
	git("mv", "README.md", "renamed.md")
	put("new\nfile.md", "new\n")
	put("ignored.log", "ignored\n")
	files, err := changedFiles(t.Context(), root)
	if err != nil || !reflect.DeepEqual(files, []string{"README.md", "new\nfile.md", "renamed.md"}) {
		t.Fatalf("rename/NUL/ignore handling: %q %v", files, err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"ddp.yaml": "pages: {}\n", "Makefile": "check:\n\t@true\n"} {
		if err := os.WriteFile(filepath.Join(nested, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run(filepath.Join(nested, "ddp.yaml"), 1)
	run(filepath.Join(root, "alternate.yaml"), 2)
}
