// Package scaffold creates registered client artifacts from explicit definitions.
package scaffold

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/oklog/ulid/v2"
)

type Result struct {
	Ref             string   `json:"ref"`
	Files           []string `json:"files"`
	RegistryChanged bool     `json:"registry_changed"`
}

const registrationPath = "internal/app/register.go"

// Go routes are registered in code. Only their existing composition file is
// replaced; client feature files retain the usual create-only behavior.
func prepareRegistration(dir *os.Root, request Request, proposal *Proposal) ([]byte, fs.FileInfo, error) {
	if request.Kind != "route" {
		return nil, nil, nil
	}
	if _, err := dir.Lstat("internal/app/" + request.Name); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return nil, nil, fmt.Errorf("client feature already exists: %s", request.Name)
		}
		return nil, nil, err
	}
	info, err := dir.Lstat(registrationPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read client registration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("client registration must be a regular file, not a symlink")
	}
	original, err := dir.ReadFile(registrationPath)
	if err != nil {
		return nil, nil, err
	}
	next, err := addRouteRegistration(original, request.Name)
	if err != nil {
		return nil, nil, err
	}
	proposal.Files[registrationPath] = next
	return original, info, nil
}

func Preview(registryPath string, request Request) (Proposal, error) {
	original, err := os.ReadFile(registryPath)
	if err != nil {
		return Proposal{}, err
	}
	cfg, err := config.Parse(original)
	if err != nil {
		return Proposal{}, err
	}
	if err := cfg.ValidateFiles(filepath.Dir(registryPath)); err != nil {
		return Proposal{}, err
	}
	proposal, err := Build(original, request)
	if err != nil {
		return Proposal{}, err
	}
	dir, err := os.OpenRoot(filepath.Dir(registryPath))
	if err != nil {
		return Proposal{}, err
	}
	defer dir.Close()
	if _, _, err := prepareRegistration(dir, request, &proposal); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

// Apply serializes scaffold commands and creates new feature files exclusively.
// The final atomic rename publishes YAML, or the Go aggregator for a route.
// Ordinary errors remove new artifacts and leave existing files unchanged.
func Apply(registryPath string, request Request) (result Result, err error) {
	dir, err := os.OpenRoot(filepath.Dir(registryPath))
	if err != nil {
		return result, err
	}
	defer dir.Close()
	name := filepath.Base(registryPath)
	info, err := dir.Lstat(name)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("registry must be a regular file, not a symlink")
	}
	lock, err := dir.OpenFile(".ddp-new.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return result, errors.New("another scaffold command is writing this project")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	original, err := dir.ReadFile(name)
	if err != nil {
		return result, err
	}
	existing, err := config.Parse(original)
	if err != nil {
		return result, err
	}
	if err := existing.ValidateFiles(filepath.Dir(registryPath)); err != nil {
		return result, err
	}
	proposal, err := Build(original, request)
	if err != nil {
		return result, err
	}
	registration, registrationInfo, err := prepareRegistration(dir, request, &proposal)
	if err != nil {
		return result, err
	}
	result = Result{Ref: proposal.Ref, Files: []string{}, RegistryChanged: !bytes.Equal(original, proposal.Registry)}
	for path := range proposal.Files {
		if !filepath.IsLocal(path) {
			return result, fmt.Errorf("scaffold path must stay inside project: %s", path)
		}
		if request.Kind == "route" && path == registrationPath {
			result.Files = append(result.Files, path)
			continue
		}
		if _, err := dir.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return result, fmt.Errorf("artifact already exists: %s", path)
			}
			return result, err
		}
		result.Files = append(result.Files, path)
	}
	sort.Strings(result.Files)
	var created, folders []string
	expected := map[string][]byte{}
	committed := false
	defer func() {
		if committed {
			return
		}
		for i := len(created) - 1; i >= 0; i-- {
			path := created[i]
			current, readErr := dir.ReadFile(path)
			if readErr == nil && !bytes.Equal(current, expected[path]) {
				err = errors.Join(err, fmt.Errorf("retained concurrently modified scaffold: %s", path))
				continue
			}
			if removeErr := dir.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
		for i := len(folders) - 1; i >= 0; i-- {
			_ = dir.Remove(folders[i])
		}
	}()
	for _, path := range result.Files {
		if request.Kind == "route" && path == registrationPath {
			continue
		}
		parent := ""
		for _, part := range strings.Split(filepath.Dir(path), string(filepath.Separator)) {
			if part == "." {
				continue
			}
			parent = filepath.Join(parent, part)
			if _, err := dir.Stat(parent); errors.Is(err, os.ErrNotExist) {
				if err := dir.Mkdir(parent, 0755); err != nil {
					return result, err
				}
				folders = append(folders, parent)
			} else if err != nil {
				return result, err
			}
		}
		file, err := dir.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return result, err
		}
		created = append(created, path)
		written, writeErr := file.Write(proposal.Files[path])
		expected[path] = proposal.Files[path][:written]
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return result, err
		}
	}
	next, err := config.Parse(proposal.Registry)
	if err != nil {
		return result, err
	}
	if err := next.ValidateFiles(filepath.Dir(registryPath)); err != nil {
		return result, err
	}
	current, err := dir.ReadFile(name)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(current, original) {
		return result, errors.New("registry changed while scaffolding; retry with the current file")
	}
	target, replacement := name, proposal.Registry
	if request.Kind == "route" {
		currentInfo, err := dir.Lstat(registrationPath)
		if err != nil {
			return result, err
		}
		current, err := dir.ReadFile(registrationPath)
		if err != nil {
			return result, err
		}
		if !os.SameFile(currentInfo, registrationInfo) || !bytes.Equal(current, registration) {
			return result, errors.New("client registration changed while scaffolding; retry with the current file")
		}
		target, replacement, info = registrationPath, proposal.Files[registrationPath], registrationInfo
	}
	if result.RegistryChanged || request.Kind == "route" {
		temporary := ".ddp-new-" + ulid.Make().String()
		file, err := dir.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return result, err
		}
		defer dir.Remove(temporary)
		_, writeErr := file.Write(replacement)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return result, err
		}
		// ponytail: a killed process can leave unregistered source files before this
		// final declaration rename; inspect and remove those files before retrying.
		if err := dir.Rename(temporary, target); err != nil {
			return result, err
		}
	}
	committed = true
	return result, nil
}
