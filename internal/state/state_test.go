package state

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestStateGenericManagedItems(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetItems([]string{"zsh", "git", "git"}, "managed", "configs")
	if got := strings.Join(s.Items("managed", "configs"), ","); got != "git,zsh" {
		t.Fatalf("managed configs = %q", got)
	}
}

func TestStateValidationRejectsUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	if err := os.WriteFile(path, []byte("version: 2\nmanaged:\n  packages: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("error = %v", err)
	}
}

func TestStateValidationRejectsMalformedManagedList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nmanaged:\n  packages: nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "managed.packages") {
		t.Fatalf("error = %v", err)
	}
}

func TestStateMigrationAddsVersionedManagedShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	if err := os.WriteFile(path, []byte("managed:\n  packages:\n    - git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Migrated || s.Data["version"] != CurrentVersion {
		t.Fatalf("migration state = %#v", s.Data)
	}
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("backup error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(loaded.Packages(), ","); got != "git" {
		t.Fatalf("migrated packages = %q", got)
	}
}

func TestStatePackageOriginsRoundTrip(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetPackageOrigins(map[string]string{"git": "repo", "yay-tool": "aur"})
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.PackageOrigins(); !reflect.DeepEqual(got, map[string]string{"git": "repo", "yay-tool": "aur"}) {
		t.Fatalf("origins = %#v", got)
	}
}
