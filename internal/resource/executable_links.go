package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

// executableLinkSources expands manifest collections into target/source pairs.
// Keeping this expansion separate makes planning and application use identical
// discovery rules.
func executableLinkSources(m *manifest.Manifest, s *state.State, options Options) (map[string]string, error) {
	links := make(map[string]string)
	for _, collection := range m.ExecutableLinkCollections(options.Profile, selectionValues(m, s)) {
		sourceDir := filepath.Join(options.RootPath, collection.Source)
		entries, err := os.ReadDir(sourceDir)
		if err != nil {
			return nil, fmt.Errorf("read executable link source %s: %w", sourceDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, fmt.Errorf("inspect executable link source %s: %w", entry.Name(), err)
			}
			if info.Mode()&0111 == 0 {
				continue
			}
			target := filepath.Join(collection.Target, collection.Prefix+entry.Name())
			links[target] = filepath.Join(sourceDir, entry.Name())
		}
	}
	return links, nil
}

func declaredExecutableLinks(m *manifest.Manifest, s *state.State, options Options) ([]string, error) {
	links, err := executableLinkSources(m, s, options)
	if err != nil {
		return nil, err
	}
	declared := make([]string, 0, len(links))
	for target := range links {
		declared = append(declared, target)
	}
	return unique(declared), nil
}

func planExecutableLinks(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
	desired, err := executableLinkSources(m, s, options)
	if err != nil {
		return StagePlan{}, err
	}
	planned := StagePlan{}
	for target := range desired {
		planned.Declared = append(planned.Declared, target)
	}
	tracked := s.Items(state.ManagedKey, state.ManagedExecutableLinks)
	for target, source := range desired {
		matches, err := executableLinkMatches(target, source)
		if err != nil {
			return StagePlan{}, err
		}
		if matches {
			if !contains(tracked, target) {
				planned.Adopted = append(planned.Adopted, target)
			}
		} else {
			planned.Missing = append(planned.Missing, target)
		}
	}
	planned.Declared = unique(planned.Declared)
	planned.Extra = subtract(tracked, planned.Declared)
	for _, collection := range m.ExecutableLinkCollections(options.Profile, selectionValues(m, s)) {
		if !collection.Prune {
			continue
		}
		entries, err := os.ReadDir(collection.Target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return StagePlan{}, fmt.Errorf("read executable link target %s: %w", collection.Target, err)
		}
		for _, entry := range entries {
			target := filepath.Join(collection.Target, entry.Name())
			if !strings.HasPrefix(entry.Name(), collection.Prefix) || contains(planned.Declared, target) || contains(planned.Extra, target) {
				continue
			}
			info, err := os.Lstat(target)
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				if _, err := os.Stat(target); os.IsNotExist(err) {
					planned.Extra = append(planned.Extra, target)
				}
			}
		}
	}
	return sortPlan(planned), nil
}

func executableLinkMatches(target, source string) (bool, error) {
	actual, err := os.Readlink(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect executable link %s: %w", target, err)
	}
	return actual == source, nil
}

func applyExecutableLinks(plan StagePlan, options Options, m *manifest.Manifest, s *state.State) error {
	desired, err := executableLinkSources(m, s, options)
	if err != nil {
		return err
	}
	for _, target := range plan.Missing {
		if err := linkExecutable(desired[target], target, options.Replace); err != nil {
			if errors.Is(err, errConfigConflict) {
				fmt.Fprintf(options.Output, "skipping %s: exists and is not a repo link; use --replace to overwrite\n", target)
				continue
			}
			return err
		}
	}
	return removeExecutableLinks(plan.Extra)
}

func removeExecutableLinks(targets []string) error {
	for _, target := range targets {
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect executable link %s: %w", target, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("remove executable link %s: %w", target, err)
			}
		}
	}
	return nil
}

func linkExecutable(source, target string, replace bool) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create executable link directory: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		matches, matchErr := executableLinkMatches(target, source)
		if matchErr != nil {
			return matchErr
		}
		if matches {
			return nil
		}
		if !replace {
			return fmt.Errorf("%w: %s", errConfigConflict, target)
		}
		info, infoErr := os.Lstat(target)
		if infoErr != nil {
			return infoErr
		}
		if info.IsDir() {
			return fmt.Errorf("%w: target is a directory: %s", errConfigConflict, target)
		}
		if err := os.Remove(target); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(source, target); err != nil {
		return fmt.Errorf("link executable %s: %w", target, err)
	}
	return nil
}
