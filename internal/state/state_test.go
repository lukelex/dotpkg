package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatePreservesSelectionsAndManagedPackages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Set(true, "current", "selections", "hyprland")
	s.SetPackages([]string{"zsh", "git", "git"})
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if selected, recorded := loaded.Selection("hyprland"); !selected || !recorded {
		t.Fatalf("selection = %v, recorded = %v", selected, recorded)
	}
	packages := loaded.Packages()
	if len(packages) != 2 || packages[0] != "git" || packages[1] != "zsh" {
		t.Fatalf("packages = %#v", packages)
	}
}

func TestStateLoadsLegacyPackageList(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "packages"), []byte("git\n\nbat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(filepath.Join(directory, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Packages(), ","); got != "bat,git" {
		t.Fatalf("legacy packages = %q", got)
	}
}
