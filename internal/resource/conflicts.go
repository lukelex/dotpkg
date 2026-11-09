package resource

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

// ConfigConflict is a target that would otherwise be preserved during sync.
type ConfigConflict struct {
	Source   string
	Target   string
	observed os.FileInfo
}

func ConfigConflicts(m *manifest.Manifest, s *state.State, options Options) ([]ConfigConflict, error) {
	var conflicts []ConfigConflict
	for _, mapping := range declaredMetadata(m, s, options.Profile, "configs") {
		source, target, err := configPaths(mapping, options)
		if err != nil {
			return nil, err
		}
		matches, err := configMatches(target, source)
		if err != nil {
			return nil, err
		}
		if matches {
			continue
		}
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		conflicts = append(conflicts, ConfigConflict{source, target, info})
	}
	return conflicts, nil
}

// Backup checks that the reviewed target is still the same object. The caller
// subsequently runs normal reconciliation, which continues preserving any new
// conflicting path created after this operation.
func (c ConfigConflict) Backup() (string, error) {
	info, err := os.Lstat(c.Target)
	if err != nil {
		return "", err
	}
	if c.observed == nil || !os.SameFile(info, c.observed) || info.Size() != c.observed.Size() || !info.ModTime().Equal(c.observed.ModTime()) {
		return "", fmt.Errorf("config changed since inspection: %s; check again", c.Target)
	}
	directory, err := os.MkdirTemp(filepath.Dir(c.Target), ".dotpkg-backup-*")
	if err != nil {
		return "", err
	}
	backup := filepath.Join(directory, filepath.Base(c.Target))
	if err := os.Rename(c.Target, backup); err != nil {
		_ = os.Remove(directory)
		return "", err
	}
	return backup, nil
}
