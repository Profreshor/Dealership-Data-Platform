package doctor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func TestDeploymentRepairIsHostOnly(t *testing.T) {
	repair := platformRepair("ddp:deployment")
	if !strings.Contains(repair, "ddp deploy status --json") || !strings.Contains(repair, "release journal") || !strings.Contains(repair, "image digest") {
		t.Fatalf("repair=%q", repair)
	}
}

func TestEnvModeAndSecretNames(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	if EnvFile(root).State != "failing" {
		t.Fatal("missing env accepted")
	}
	if err := os.WriteFile(path, []byte("SECRET=never-print-this"), 0600); err != nil {
		t.Fatal(err)
	}
	if EnvFile(root).State != "ok" {
		t.Fatal("private env rejected")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if EnvFile(root).State != "failing" {
		t.Fatal("public env accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", path); err != nil {
		t.Fatal(err)
	}
	if EnvFile(root).State != "failing" {
		t.Fatal("symlink accepted")
	}
	cfg := &config.Config{Integrations: map[string]config.Integration{"a": {Auth: &config.IntegrationAuth{Secret: "TEST_DOCTOR_TOKEN"}}, "b": {Auth: &config.IntegrationAuth{Secret: "TEST_DOCTOR_TOKEN"}}}}
	t.Setenv("DATABASE_URL", "sensitive-connection-string")
	t.Setenv("TEST_DOCTOR_TOKEN", "")
	if got := Secrets(cfg); got.State != "failing" || got.Value != "TEST_DOCTOR_TOKEN" {
		t.Fatalf("missing: %+v", got)
	}
	t.Setenv("TEST_DOCTOR_TOKEN", "never-print-this")
	data, _ := json.Marshal(Secrets(cfg))
	if Secrets(cfg).State != "ok" || strings.Contains(string(data), "never-print") || strings.Contains(string(data), "sensitive") {
		t.Fatalf("secret output: %s", data)
	}
}

func TestClockBoundsAccountForRoundTrip(t *testing.T) {
	start := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)
	for _, tc := range []struct {
		observed time.Time
		want     string
	}{
		{start, "ok"}, {start.Add(-5 * time.Second), "ok"}, {end.Add(5 * time.Second), "ok"},
		{start.Add(-6 * time.Second), "failing"}, {end.Add(6 * time.Second), "failing"},
	} {
		if got := clockResult(start, end, tc.observed); got.State != tc.want {
			t.Fatalf("%v: %+v", tc, got)
		}
	}
	if clockResult(end, start, start).State != "unknown" {
		t.Fatal("clock jump accepted")
	}
}

func gitFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal/ddp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/ddp/base.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	rev, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(string(rev))
}

func TestTemplateDriftIsLocalAndIgnoresClientPaths(t *testing.T) {
	root, revision := gitFixture(t)
	check := func(want string) {
		t.Helper()
		if got := Template(t.Context(), root, revision); got.State != want {
			t.Fatalf("want %s: %+v", want, got)
		}
	}
	check("ok")
	if err := os.WriteFile(filepath.Join(root, "ddp.yaml"), []byte("client config"), 0600); err != nil {
		t.Fatal(err)
	}
	check("ok")
	file := filepath.Join(root, "internal/ddp/new.txt")
	if err := os.WriteFile(file, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	check("failing")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/ddp/base.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	check("failing")
	if Template(t.Context(), root, strings.Repeat("0", 40)).State != "unknown" {
		t.Fatal("missing revision accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if Template(ctx, root, revision).State != "unknown" {
		t.Fatal("cancelled git accepted")
	}
}
