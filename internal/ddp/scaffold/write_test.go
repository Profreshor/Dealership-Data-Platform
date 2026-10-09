package scaffold

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
)

func project(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ddp.yaml")
	original := append([]byte("# Preserve client notes.\n"), baseRegistry(t)...)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	return path, original
}

func jobRequest() Request {
	return Request{Kind: "job", Name: "check_records", Definition: []byte("purpose: Check records\naction: check\n")}
}

func TestPreviewAndApply(t *testing.T) {
	path, original := project(t)
	proposal, err := Preview(path, jobRequest())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("preview changed project: %v %v", entries, err)
	}
	got, err := Apply(path, jobRequest())
	if err != nil || !got.RegistryChanged || got.Ref != "job/check_records" || len(got.Files) != 1 {
		t.Fatalf("apply: %+v %v", got, err)
	}
	written, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(written, proposal.Registry) || !strings.Contains(string(written), "# Preserve client notes.") {
		t.Fatalf("registry publication: %s %v", written, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("registry permissions changed: %v %v", info, err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateFiles(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(path, jobRequest()); err == nil {
		t.Fatal("duplicate resource accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, written) || bytes.Equal(after, original) {
		t.Fatal("duplicate changed registry", err)
	}
}

func TestApplyRefusesExistingArtifactAndEscapingPaths(t *testing.T) {
	for _, mode := range []string{"file", "directory_symlink", "registry_symlink", "lock_symlink", "locked"} {
		t.Run(mode, func(t *testing.T) {
			path, original := project(t)
			root := filepath.Dir(path)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "file":
				if err := os.Mkdir(filepath.Join(root, "jobs"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "jobs/check_records.py"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory_symlink":
				if err := os.Symlink(outside, filepath.Join(root, "jobs")); err != nil {
					t.Fatal(err)
				}
			case "registry_symlink":
				if err := os.WriteFile(sentinel, original, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, path); err != nil {
					t.Fatal(err)
				}
			case "lock_symlink":
				if err := os.Symlink(sentinel, filepath.Join(root, ".ddp-new.lock")); err != nil {
					t.Fatal(err)
				}
			case "locked":
				f, err := os.OpenFile(filepath.Join(root, ".ddp-new.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			}
			if _, err := Apply(path, jobRequest()); err == nil {
				t.Fatal("unsafe publication accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("original registry changed", err)
			}
			if mode != "registry_symlink" {
				data, err := os.ReadFile(sentinel)
				if err != nil || string(data) != "keep" {
					t.Fatal("outside file changed", err)
				}
			}
			if mode == "file" {
				data, err := os.ReadFile(filepath.Join(root, "jobs/check_records.py"))
				if err != nil || string(data) != "keep" {
					t.Fatal("existing source changed", err)
				}
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatal("created file outside project", err)
			}
		})
	}
}

func TestApplyRollsBackWhenRegistryCannotBePublished(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	path, original := project(t)
	root := filepath.Dir(path)
	if err := os.Mkdir(filepath.Join(root, "jobs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ddp-new.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0755)
	if _, err := Apply(path, jobRequest()); err == nil {
		t.Fatal("published inside unwritable directory")
	}
	if _, err := os.Stat(filepath.Join(root, "jobs/check_records.py")); !os.IsNotExist(err) {
		t.Fatal("partial source survived rollback", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("registry changed on rollback", err)
	}
}
