package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTemplateTracksAllSkillProvenancePaths(t *testing.T) {
	paths := []string{"skills", ".agents/skills", ".claude/skills"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			root, revision := gitFixture(t)
			file := filepath.Join(root, path, "fixture.md")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				{"add", path},
				{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "provenance"},
			} {
				if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git fixture: %v %s", err, out)
				}
			}
			if got := Template(t.Context(), root, revision); got.State != "failing" {
				t.Fatalf("Template(%q) = %+v", path, got)
			}
		})
	}
}

func TestTemplateTracksUntrackedSkillProvenancePath(t *testing.T) {
	root, revision := gitFixture(t)
	file := filepath.Join(root, ".agents/skills", "untracked.md")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Template(t.Context(), root, revision); got.State != "failing" {
		t.Fatalf("untracked provenance file was missed: %+v", got)
	}
}
