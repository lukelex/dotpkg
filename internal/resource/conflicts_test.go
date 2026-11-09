package resource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConflictBackupPreservesContentsAndRejectsChangedTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "config")
	if err := os.WriteFile(target, []byte("personal config"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Lstat(target)
	conflict := ConfigConflict{Target: target, observed: info}
	backup, err := conflict.Backup()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(backup)
	if err != nil || string(data) != "personal config" {
		t.Fatalf("backup=%q error=%v", data, err)
	}
	if err := os.WriteFile(target, []byte("new config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := conflict.Backup(); err == nil {
		t.Fatal("backed up a replacement that was never reviewed")
	}
	data, _ = os.ReadFile(target)
	if string(data) != "new config" {
		t.Fatal("changed target was modified")
	}
}
