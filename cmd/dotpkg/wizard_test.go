package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWizardWithoutManifestPreviewsStarter(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("DOTPKG_MANIFEST", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	if err := wizardCommand(context.Background(), []string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("preview wrote files: %v, %v", entries, err)
	}
	if err := wizardCommand(context.Background(), []string{"--dry-run", "--manifest", "missing.yaml"}); err == nil {
		t.Fatal("explicit missing manifest silently replaced")
	}
	t.Setenv("DOTPKG_MANIFEST", "missing.yaml")
	if err := wizardCommand(context.Background(), []string{"--dry-run"}); err == nil {
		t.Fatal("environment manifest silently replaced")
	}
	t.Setenv("DOTPKG_MANIFEST", "")
	if err := os.WriteFile("packages.yaml", []byte("common:\n  packages:\n    headless:\n      git: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := wizardCommand(context.Background(), []string{"--dry-run"}); err != nil {
		t.Fatalf("default existing manifest: %v", err)
	}
}
