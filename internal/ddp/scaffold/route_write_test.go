package scaffold

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func routeProject(t *testing.T) (string, []byte, []byte) {
	t.Helper()
	path, registry := project(t)
	source, err := os.ReadFile("../../app/register.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(path), "internal/app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), registrationPath), source, 0600); err != nil {
		t.Fatal(err)
	}
	return path, registry, source
}

func routeRequest() Request {
	return Request{Kind: "route", Name: "example", Definition: []byte("pattern: GET /api/example\npolicy: authenticated\n")}
}

func TestRoutePublicationPreservesYAMLAndMatchesPreview(t *testing.T) {
	path, registry, registration := routeProject(t)
	root := filepath.Dir(path)
	preview, err := Preview(path, routeRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertFile := func(path string, want []byte) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: got %q, want %q (%v)", path, got, want, err)
		}
	}
	assertFile(filepath.Join(root, registrationPath), registration)
	assertFile(path, registry)
	if _, err := os.Stat(filepath.Join(root, "internal/app/example")); !os.IsNotExist(err) {
		t.Fatalf("preview created feature: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".ddp-new.lock")); !os.IsNotExist(err) {
		t.Fatalf("preview created lock: %v", err)
	}
	result, err := Apply(path, routeRequest())
	if err != nil || result.RegistryChanged || result.Ref != "route/example" || len(result.Files) != 2 {
		t.Fatalf("apply: %+v (%v)", result, err)
	}
	assertFile(path, registry)
	for file, expected := range preview.Files {
		assertFile(filepath.Join(root, file), expected)
	}
	info, err := os.Stat(filepath.Join(root, registrationPath))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("registration permissions changed: %v (%v)", info, err)
	}
	if _, err := Apply(path, routeRequest()); err == nil {
		t.Fatal("duplicate feature accepted")
	}
	assertFile(filepath.Join(root, registrationPath), preview.Files[registrationPath])
}

func TestRoutePublicationRefusesUnsafeTargets(t *testing.T) {
	for _, mode := range []string{"existing_feature", "feature_symlink", "registration_symlink", "malformed_registration"} {
		t.Run(mode, func(t *testing.T) {
			path, registry, registration := routeProject(t)
			root := filepath.Dir(path)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, registration, 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "existing_feature":
				if err := os.Mkdir(filepath.Join(root, "internal/app/example"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "internal/app/example/register.go"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "feature_symlink":
				if err := os.Symlink(outside, filepath.Join(root, "internal/app/example")); err != nil {
					t.Fatal(err)
				}
			case "registration_symlink":
				if err := os.Remove(filepath.Join(root, registrationPath)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, filepath.Join(root, registrationPath)); err != nil {
					t.Fatal(err)
				}
			case "malformed_registration":
				registration = []byte("package app\nfunc Register() {}\n")
				if err := os.WriteFile(filepath.Join(root, registrationPath), registration, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Apply(path, routeRequest()); err == nil {
				t.Fatal("unsafe publication accepted")
			}
			for file, want := range map[string][]byte{path: registry, filepath.Join(root, registrationPath): registration} {
				got, err := os.ReadFile(file)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("existing file changed: %s (%v)", file, err)
				}
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("outside directory changed: %v (%v)", entries, err)
			}
		})
	}
}

func TestRoutePublicationRollsBackBeforeRegistrationRename(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	path, registry, registration := routeProject(t)
	root := filepath.Dir(path)
	if err := os.WriteFile(filepath.Join(root, ".ddp-new.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	// The feature directory is writable, but staging the final publication fails.
	if err := os.Chmod(root, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0755) })
	if _, err := Apply(path, routeRequest()); err == nil {
		t.Fatal("published inside unwritable directory")
	}
	if _, err := os.Stat(filepath.Join(root, "internal/app/example")); !os.IsNotExist(err) {
		t.Fatalf("partial feature survived rollback: %v", err)
	}
	for file, want := range map[string][]byte{path: registry, filepath.Join(root, registrationPath): registration} {
		got, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("existing file changed: %s (%v)", file, err)
		}
	}
}
