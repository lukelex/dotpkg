package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/backend"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/reconcile"
	"github.com/lukelex/dotpkg/internal/state"
)

func TestChooseScopeUsesProfileByDefaultInDryRun(t *testing.T) {
	var output strings.Builder
	options := reconcile.Options{
		Profile: "server",
		DryRun:  true,
		Output:  &output,
	}

	scope, err := chooseScope(options, "ripgrep")
	if err != nil {
		t.Fatal(err)
	}
	if scope != "server" {
		t.Fatalf("scope = %q, want server", scope)
	}
	if got := output.String(); got != "dry-run: default package scope is server\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestChooseScopePromptsAndAcceptsExplicitScope(t *testing.T) {
	var output strings.Builder
	options := reconcile.Options{
		Profile: "desktop",
		Input:   strings.NewReader("common\n"),
		Output:  &output,
	}

	scope, err := chooseScope(options, "ripgrep")
	if err != nil {
		t.Fatal(err)
	}
	if scope != "common" {
		t.Fatalf("scope = %q, want common", scope)
	}
	if got := output.String(); got != "Available scopes: desktop, hyprland, i3, extras, common, option:NAME\nAdd ripgrep to scope [desktop]: " {
		t.Fatalf("output = %q", got)
	}
}

func TestChooseScopeDefaultsAfterEmptyAnswer(t *testing.T) {
	options := reconcile.Options{
		Profile: "desktop",
		Input:   strings.NewReader("\n"),
		Output:  &strings.Builder{},
	}

	scope, err := chooseScope(options, "ripgrep")
	if err != nil {
		t.Fatal(err)
	}
	if scope != "desktop" {
		t.Fatalf("scope = %q, want desktop", scope)
	}
}

func TestScopePathSupportsCommonForBothProfiles(t *testing.T) {
	for _, profile := range []string{"desktop", "server"} {
		path, err := scopePath(nil, profile, "common")
		if err != nil {
			t.Fatalf("profile %s: %v", profile, err)
		}
		if path != "common.packages.headless" {
			t.Fatalf("profile %s: path = %q", profile, path)
		}
	}
}

func TestScopePathRejectsInvalidOptionName(t *testing.T) {
	if _, err := scopePath(nil, "desktop", "option:bad.name"); err == nil {
		t.Fatal("invalid option scope was accepted")
	}
}

type addFakeBackend struct {
	installed map[string]bool
	install   []string
}

func (f *addFakeBackend) IsInstalled(_ context.Context, name string) (bool, error) {
	return f.installed[name], nil
}

func (f *addFakeBackend) RepositoryPackages(_ context.Context) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func (f *addFakeBackend) AURPackages(_ context.Context, names []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result, nil
}

func (f *addFakeBackend) Install(_ context.Context, repository, aur []string, _ string) error {
	f.install = append(f.install, repository...)
	f.install = append(f.install, aur...)
	return nil
}

func (f *addFakeBackend) Remove(_ context.Context, _ []string, _ string) error { return nil }

var _ backend.Backend = (*addFakeBackend)(nil)

func writeAddFixture(t *testing.T, directory string) (string, string) {
	t.Helper()
	manifestPath := filepath.Join(directory, "packages.yaml")
	if err := os.WriteFile(manifestPath, []byte(`source: aur
common:
  packages:
    headless:
      git: {}
profiles:
  desktop:
    packages:
      desktop:
        base: {}
      extras: {}
      hyprland: {}
      i3: {}
    options: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "state.yaml")
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{"extras", "hyprland", "i3"} {
		s.Set(false, "current", "selections", selection)
	}
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	return manifestPath, statePath
}

func TestAddWritesBaseManifestAndTracksPackage(t *testing.T) {
	manifestPath, statePath := writeAddFixture(t, t.TempDir())
	fake := &addFakeBackend{installed: map[string]bool{"git": true, "base": true}}
	if err := addLocked(context.Background(), "new-package", "desktop", reconcile.Options{
		ManifestPath: manifestPath,
		StatePath:    statePath,
		Profile:      "desktop",
		Yes:          true,
		Output:       io.Discard,
	}, fake); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasPackage("new-package") {
		t.Fatal("new package was not added to the base manifest")
	}
	if len(fake.install) != 1 || fake.install[0] != "new-package" {
		t.Fatalf("installed packages = %#v", fake.install)
	}
}

func TestAddWritesHostOverlayAndRecordsHostName(t *testing.T) {
	directory := t.TempDir()
	manifestPath, statePath := writeAddFixture(t, directory)
	hostDirectory := filepath.Join(directory, "hosts")
	if err := os.Mkdir(hostDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	hostPath := filepath.Join(hostDirectory, "laptop.yaml")
	if err := os.WriteFile(hostPath, []byte("profiles:\n  desktop:\n    packages:\n      desktop:\n        host-tool: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &addFakeBackend{installed: map[string]bool{"git": true, "base": true, "host-tool": true}}
	if err := addLocked(context.Background(), "new-package", "desktop", reconcile.Options{
		ManifestPath: manifestPath,
		HostPath:     "laptop",
		StatePath:    statePath,
		Profile:      "desktop",
		Yes:          true,
		Output:       io.Discard,
	}, fake); err != nil {
		t.Fatal(err)
	}
	base, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if base.HasPackage("new-package") {
		t.Fatal("host package was written to the base manifest")
	}
	host, err := manifest.Load(hostPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !host.HasPackage("new-package") {
		t.Fatal("new package was not added to the host overlay")
	}
	loaded, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if hostName, ok := loaded.Get("current", "host"); !ok || hostName != "laptop" {
		t.Fatalf("recorded host = %#v", hostName)
	}
}
