package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/backend"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

type fakeBackend struct {
	installed map[string]bool
	install   []string
	remove    []string
}

func TestWithLockRejectsConcurrentOperation(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	options := Options{StatePath: statePath, Profile: "desktop"}

	err := WithLock(options, func(_ Options) error {
		return WithLock(options, func(_ Options) error {
			return nil
		})
	})
	if err == nil || !strings.Contains(err.Error(), "state is locked") {
		t.Fatalf("nested lock error = %v", err)
	}
}

func TestWithLockSkipsLockForDryRun(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	options := Options{StatePath: statePath, Profile: "desktop", DryRun: true}

	if err := WithLock(options, func(normalized Options) error {
		if !normalized.DryRun {
			t.Fatal("dry-run flag was lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock file stat error = %v", err)
	}
}

func TestLoadResolvesNamedHostAndPreservesSharedState(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "packages.yaml")
	if err := os.WriteFile(manifestPath, []byte(`source: repo
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
	if err := os.Mkdir(filepath.Join(directory, "hosts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "hosts", "laptop.yaml"), []byte(`profiles:
  desktop:
    packages:
      desktop:
        host-tool: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "state.yaml")
	if err := os.WriteFile(statePath, []byte(`version: 1
current:
  selections:
    extras: false
    hyprland: false
    i3: false
managed:
  packages: []
  groups:
    - docker
  configs:
    - source:target
  services:
    - sshd
`), 0o644); err != nil {
		t.Fatal(err)
	}

	m, s, options, err := Load(Options{
		ManifestPath: manifestPath,
		HostPath:     "laptop",
		StatePath:    statePath,
		Profile:      "desktop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.HostPath != filepath.Join(directory, "hosts", "laptop.yaml") {
		t.Fatalf("host path = %q", options.HostPath)
	}
	if options.HostLabel != "laptop" {
		t.Fatalf("host label = %q", options.HostLabel)
	}
	if got := strings.Join(DeclaredPackages(m, PackageCategories(m, "desktop", true, s)), ","); got != "base,git,host-tool" {
		t.Fatalf("declared packages = %q", got)
	}
	if groups, ok := s.Get("managed", "groups"); !ok || !reflect.DeepEqual(groups, []any{"docker"}) {
		t.Fatalf("managed groups = %#v", groups)
	}
}

func TestLoadRejectsUnknownHostOverlay(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "packages.yaml")
	if err := os.WriteFile(manifestPath, []byte("source: repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := Load(Options{ManifestPath: manifestPath, HostPath: "missing"})
	if err == nil || !strings.Contains(err.Error(), "unknown host overlay: missing") {
		t.Fatalf("error = %v", err)
	}
}

func TestSyncAdoptsInstalledPackagesOnFirstRun(t *testing.T) {
	manifestPath := testManifest(t)
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	fake := &fakeBackend{installed: map[string]bool{
		"git": true,
		"bat": true,
	}}
	if err := Sync(context.Background(), Options{
		ManifestPath: manifestPath,
		StatePath:    statePath,
		Profile:      "desktop",
		Yes:          true,
	}, fake); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(loaded.Packages(), ","); got != "bat,git" {
		t.Fatalf("adopted packages = %q", got)
	}
	if len(fake.install) != 0 {
		t.Fatalf("installed already-present packages = %#v", fake.install)
	}
}

func (f *fakeBackend) IsInstalled(_ context.Context, name string) (bool, error) {
	return f.installed[name], nil
}

func (f *fakeBackend) RepositoryPackages(_ context.Context) (map[string]struct{}, error) {
	return map[string]struct{}{"git": {}, "bat": {}, "new-package": {}}, nil
}

func (f *fakeBackend) AURPackages(_ context.Context, names []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result, nil
}

func (f *fakeBackend) Install(_ context.Context, repository, aur []string, _ string) error {
	f.install = append(f.install, repository...)
	f.install = append(f.install, aur...)
	return nil
}

func (f *fakeBackend) Remove(_ context.Context, packages []string, _ string) error {
	f.remove = append(f.remove, packages...)
	return nil
}

var _ backend.Backend = (*fakeBackend)(nil)

func testManifest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "packages.yaml")
	contents := `source: repo
common:
  packages:
    headless:
      git: {}
profiles:
  desktop:
    packages:
      desktop:
        bat: {}
      extras:
        extra: {}
    options: {}
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testState(t *testing.T, manifestPath string) (*manifest.Manifest, *state.State) {
	t.Helper()
	m, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.yaml")
	s, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Set(false, "current", "selections", "extras")
	s.Set(false, "current", "selections", "hyprland")
	s.Set(false, "current", "selections", "i3")
	s.SetPackages([]string{"git", "old-package"})
	return m, s
}

func TestBuildPlanAdoptsMissingAndRemovesOnlyManaged(t *testing.T) {
	manifestPath := testManifest(t)
	m, s := testState(t, manifestPath)
	fake := &fakeBackend{installed: map[string]bool{"git": true, "bat": false}}
	plan, err := BuildPlan(context.Background(), m, s, "desktop", fake)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Missing, ",") != "bat" {
		t.Fatalf("missing = %#v", plan.Missing)
	}
	if strings.Join(plan.Extra, ",") != "old-package" {
		t.Fatalf("extra = %#v", plan.Extra)
	}
	if len(plan.Adopted) != 0 {
		t.Fatalf("adopted = %#v", plan.Adopted)
	}
}

func TestCurrentDotfilesManifestHasSameSelectedPackageCount(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "dotfiles-packages.yaml")
	m, err := manifest.Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(filepath.Join(t.TempDir(), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{"extras", "hyprland", "i3"} {
		s.Set(true, "current", "selections", selection)
	}
	for _, option := range m.OptionNames() {
		s.Set(true, "current", "selections", "options", option)
	}
	packages := DeclaredPackages(m, PackageCategories(m, "desktop", true, s))
	if len(packages) != 142 {
		t.Fatalf("selected package count = %d, want 142", len(packages))
	}
}

func TestSyncUpdatesStateAfterApply(t *testing.T) {
	manifestPath := testManifest(t)
	_, s := testState(t, manifestPath)
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBackend{installed: map[string]bool{"git": true, "bat": false}}
	options := Options{
		ManifestPath: manifestPath,
		StatePath:    s.Path,
		Profile:      "desktop",
		Yes:          true,
	}
	if err := Sync(context.Background(), options, fake); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fake.install, ",") != "bat" {
		t.Fatalf("installed = %#v", fake.install)
	}
	if strings.Join(fake.remove, ",") != "old-package" {
		t.Fatalf("removed = %#v", fake.remove)
	}
	loaded, err := state.Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(loaded.Packages(), ","); got != "bat,git" {
		t.Fatalf("managed packages = %q", got)
	}
}
