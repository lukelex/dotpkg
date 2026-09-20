package reconcile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
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
			owner := "unknown owner"
			if contents, readErr := os.ReadFile(lockPath); readErr == nil && len(contents) > 0 {
				owner = string(contents)
			}
			return fmt.Errorf("state is locked by another dotpkg process: %s (%s)", normalized.StatePath, owner)
		}
		return fmt.Errorf("lock state: %w", err)
	}
	if err := lockOwner(lockFile); err != nil {
		return fmt.Errorf("record state lock owner: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	}()

	return fn(normalized)
}

func lockOwner(file *os.File) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err := fmt.Fprintf(file, "pid=%d started=%s", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	return err
}
