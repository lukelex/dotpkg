package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WithLock serializes operations that can update the manifest or package state.
// The lock is advisory between dotpkg processes and is held for the duration of
// fn. Dry-runs do not create or acquire a lock because they do not write.
func WithLock(options Options, fn func(Options) error) error {
	normalized, err := options.normalize()
	if err != nil {
		return err
	}
	if normalized.DryRun {
		return fn(normalized)
	}

	if err := os.MkdirAll(filepath.Dir(normalized.StatePath), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	lockPath := normalized.StatePath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return fmt.Errorf("state is locked by another dotpkg process: %s", normalized.StatePath)
		}
		return fmt.Errorf("lock state: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	return fn(normalized)
}
