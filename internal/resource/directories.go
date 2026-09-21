package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

func declaredDirectories(m *manifest.Manifest, s *state.State, options Options) ([]string, error) {
	var result []string
	for _, directory := range m.ResourceDirectories(options.Profile, selectionValues(m, s)) {
		path, err := userDirectoryPath(directory)
		if err != nil {
			return nil, err
		}
		result = append(result, path)
	}
	return unique(result), nil
}

func planDirectories(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
	declared, err := declaredDirectories(m, s, options)
	if err != nil {
		return StagePlan{}, err
	}
	planned := StagePlan{Declared: declared}
	tracked := s.Items(state.ManagedKey, state.ManagedDirectories)
	for _, directory := range declared {
		info, err := os.Lstat(directory)
		switch {
		case os.IsNotExist(err):
			planned.Missing = append(planned.Missing, directory)
		case err != nil:
			return StagePlan{}, fmt.Errorf("inspect directory %s: %w", directory, err)
		case info.IsDir():
			if !contains(tracked, directory) {
				planned.Adopted = append(planned.Adopted, directory)
			}
		default:
			return StagePlan{}, fmt.Errorf("directory target is not a directory: %s", directory)
		}
	}
	planned.Extra = subtract(tracked, declared)
	return sortPlan(planned), nil
}

func applyDirectories(plan StagePlan) error {
	for _, directory := range plan.Missing {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", directory, err)
		}
	}
	return nil
}

func cleanDirectories(directories []string) error {
	for _, directory := range directories {
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect directory %s: %w", directory, err)
		}
		if !info.IsDir() {
			continue
		}
		if err := os.Remove(directory); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			// Non-empty directories are intentionally retained.
			if errors.Is(err, syscall.ENOTEMPTY) {
				continue
			}
			return fmt.Errorf("remove directory %s: %w", directory, err)
		}
	}
	return nil
}

func userDirectoryPath(value string) (string, error) {
	path, roots, err := resolveUserPath(value, true)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("directory target must be absolute or use $HOME: %s", value)
	}
	if !roots.contains(path) {
		return "", fmt.Errorf("directory target is outside the user home: %s", value)
	}
	return path, nil
}
