package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/spf13/cobra"
)

type configChange struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type configDiffReport struct {
	Baseline string         `json:"baseline"`
	Current  string         `json:"current"`
	Changed  bool           `json:"changed"`
	Changes  []configChange `json:"changes"`
}

func configDiffCommand(catalog commandCatalog, registry *string, write func(any) error) *cobra.Command {
	return catalog.declare(localRead, &cobra.Command{Use: "diff [baseline-file]", Short: "Compare project configuration with a baseline", Long: "Compare validated registry values with the same file in Git HEAD, or with an explicit baseline file without Git. Ignores YAML formatting and comments; does not read historical entrypoints or resolve secrets. Reports sorted JSON Pointer changes and exits 3 when declarations differ, 0 when equal. Does not inspect deployed state.", Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 {
			return usageError{fmt.Errorf("accepts at most one baseline file")}
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		currentPath, err := canonicalPath(*registry)
		if err != nil {
			return fmt.Errorf("current config: %w", err)
		}
		currentBytes, err := os.ReadFile(currentPath)
		if err != nil {
			return fmt.Errorf("read current config %q: %w", currentPath, err)
		}
		current, err := normalizedConfig(currentBytes)
		if err != nil {
			return fmt.Errorf("current config %q: %w", currentPath, err)
		}
		baselineLabel := ""
		var baseline []byte
		if len(args) == 1 {
			baselinePath, pathErr := canonicalPath(args[0])
			if pathErr != nil {
				return fmt.Errorf("baseline config: %w", pathErr)
			}
			baseline, err = os.ReadFile(baselinePath)
			if err != nil {
				return fmt.Errorf("read baseline config %q: %w", baselinePath, err)
			}
			baselineLabel = baselinePath
		} else {
			root, rootErr := gitRoot(cmd.Context(), filepath.Dir(currentPath))
			if rootErr != nil {
				return rootErr
			}
			rel, relErr := filepath.Rel(root, currentPath)
			if relErr != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("current config is outside git repository %q", root)
			}
			baseline, err = gitShow(cmd, root, filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			baselineLabel = "HEAD:" + filepath.ToSlash(rel)
		}
		baselineJSON, err := normalizedConfig(baseline)
		if err != nil {
			return fmt.Errorf("baseline config %q: %w", baselineLabel, err)
		}
		changes := diffJSON(baselineJSON, current)
		report := configDiffReport{Baseline: baselineLabel, Current: currentPath, Changed: len(changes) > 0, Changes: changes}
		if err := write(report); err != nil {
			return err
		}
		if report.Changed {
			return unhealthyReport{}
		}
		return nil
	}})
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func normalizedConfig(data []byte) (any, error) {
	cfg, err := config.Parse(data)
	if err != nil {
		return nil, err
	}
	var value any
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func gitRoot(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find git HEAD for %q: %w", dir, err)
	}
	return filepath.EvalSymlinks(strings.TrimSuffix(string(out), "\n"))
}
func gitShow(cmd *cobra.Command, root, path string) ([]byte, error) {
	out, err := exec.CommandContext(cmd.Context(), "git", "-C", root, "show", "HEAD:"+path).Output()
	if err != nil {
		return nil, fmt.Errorf("read git HEAD:%s: %w", path, err)
	}
	return out, nil
}

func diffJSON(before, after any) []configChange {
	changes := make([]configChange, 0)
	walkDiff("", before, after, &changes)
	slices.SortFunc(changes, func(a, b configChange) int { return strings.Compare(a.Path, b.Path) })
	return changes
}

func walkDiff(path string, before, after any, changes *[]configChange) {
	bMap, bOK := before.(map[string]any)
	aMap, aOK := after.(map[string]any)
	if bOK && aOK {
		for key, b := range bMap {
			child := path + "/" + escapeJSONPointer(key)
			a, aok := aMap[key]
			if !aok {
				*changes = append(*changes, configChange{Path: child, Kind: "remove", Before: b})
			} else {
				walkDiff(child, b, a, changes)
			}
		}
		for key, a := range aMap {
			if _, exists := bMap[key]; !exists {
				*changes = append(*changes, configChange{Path: path + "/" + escapeJSONPointer(key), Kind: "add", After: a})
			}
		}
		return
	}
	if reflect.DeepEqual(before, after) {
		return
	}
	*changes = append(*changes, configChange{Path: path, Kind: "replace", Before: before, After: after})
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
