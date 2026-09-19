package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentDotfilesManifestShape(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "dotfiles-packages.yaml")
	m, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Source(); got != "aur" {
		t.Fatalf("source = %q, want aur", got)
	}
	if !contains(m.PackageNames("common.packages.headless"), "git") {
		t.Fatal("common package git was not found")
	}
	if !contains(m.PackageNames("profiles.desktop.packages.desktop"), "google-chrome") {
		t.Fatal("desktop package google-chrome was not found")
	}
	if got := m.PackageOrigin("google-chrome"); got != "aur" {
		t.Fatalf("google-chrome origin = %q, want aur", got)
	}
	if got := m.PackageOrigin("git"); got != "aur" {
		t.Fatalf("git origin = %q, want aur", got)
	}
	if got := m.Value("profiles.desktop.packages.hyprland.hyprland.configs"); got == nil {
		t.Fatal("hyprland config metadata was not retained")
	}
}

func TestHostOverlayDeepMerge(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "packages.yaml")
	overlay := filepath.Join(directory, "host.yaml")
	if err := os.WriteFile(base, []byte("source: aur\nprofiles:\n  desktop:\n    packages:\n      desktop:\n        base-package: {}\n      extras:\n        base-extra: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("profiles:\n  desktop:\n    packages:\n      extras:\n        host-extra: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.PackageNames("profiles.desktop.packages.desktop"), ","); got != "base-package" {
		t.Fatalf("base package names = %q", got)
	}
	if got := strings.Join(m.PackageNames("profiles.desktop.packages.extras"), ","); got != "base-extra,host-extra" {
		t.Fatalf("merged extra package names = %q", got)
	}
}

func TestAddPackageWritesTargetManifest(t *testing.T) {
	source := filepath.Join("..", "..", "testdata", "dotfiles-packages.yaml")
	target := filepath.Join(t.TempDir(), "packages.yaml")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(target, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddPackage(target, "profiles.desktop.packages.desktop", "test-package"); err != nil {
		t.Fatal(err)
	}
	m, err = Load(target, "")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(m.PackageNames("profiles.desktop.packages.desktop"), "test-package") {
		t.Fatal("added package was not written")
	}
}

func TestResourceMetadataIsRetainedAndQueryable(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "dotfiles-packages.yaml")
	m, err := Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	groups := m.MetadataStrings("common.packages", "groups")
	if !contains(groups, "docker") {
		t.Fatalf("groups = %#v", groups)
	}
	configs := m.MetadataStrings("profiles.desktop.packages.i3", "configs")
	if !contains(configs, "linux/xinitrc:$HOME/.xinitrc") {
		t.Fatalf("configs = %#v", configs)
	}
	services := m.ServiceNames("profiles.desktop.packages.desktop", "system")
	if !contains(services, "bluetooth") {
		t.Fatalf("services = %#v", services)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
