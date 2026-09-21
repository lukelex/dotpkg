package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/appimage"
	"github.com/lukelex/dotpkg/internal/backend"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/recovery"
	"github.com/lukelex/dotpkg/internal/resource"
	"github.com/lukelex/dotpkg/internal/state"
)

type fakeBackend struct {
	installed     map[string]bool
	install       []string
	remove        []string
	removeProfile string
	installErr    error
}

func TestPlanDocumentUsesVersionedUnifiedSchema(t *testing.T) {
	document := NewPlanDocument(Plan{Missing: []string{"git"}}, &resource.Plan{
		Configs: resource.StagePlan{Extra: []string{"config/old:$HOME/.old"}},
	}, Options{ManifestPath: "packages.yaml", Profile: "server"})
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
		Changes int    `json:"changes"`
		Stages  []struct {
			Name    string `json:"name"`
			Changes []any  `json:"changes"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != "dotpkg.plan" || decoded.Version != 1 || decoded.Changes != 2 || len(decoded.Stages) != 4 {
		t.Fatalf("document = %s", encoded)
	}
	if decoded.Stages[0].Name != "packages" || len(decoded.Stages[0].Changes) != 1 {
		t.Fatalf("package stage = %#v", decoded.Stages[0])
	}
	golden, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "plan-document.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), strings.TrimSpace(string(golden)); got != want {
		t.Fatalf("plan document = %s\nwant = %s", got, want)
	}
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
	if !strings.Contains(err.Error(), "pid=") {
		t.Fatalf("lock diagnostics = %v", err)
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
		"git":   true,
		"bat":   true,
		"extra": true,
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
	if got := strings.Join(loaded.Packages(), ","); got != "bat,extra,git" {
		t.Fatalf("adopted packages = %q", got)
	}
	if len(fake.install) != 0 {
		t.Fatalf("installed already-present packages = %#v", fake.install)
	}
}

func TestCleanRemovesOnlyManagedUndeclaredPackages(t *testing.T) {
	manifestPath := testManifest(t)
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPackages([]string{"git", "old-package"})
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBackend{installed: map[string]bool{"git": true, "old-package": true}}
	if err := Clean(context.Background(), Options{
		ManifestPath: manifestPath,
		StatePath:    statePath,
		Profile:      "desktop",
		Yes:          true,
	}, fake); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.remove, []string{"old-package"}) {
		t.Fatalf("removed packages = %#v", fake.remove)
	}
	loaded, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Packages(); !reflect.DeepEqual(got, []string{"git"}) {
		t.Fatalf("remaining packages = %#v", got)
	}
}

func TestSyncRollsBackPartiallyInstalledPackages(t *testing.T) {
	manifestPath := testManifest(t)
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	fake := &fakeBackend{installed: map[string]bool{}, installErr: errors.New("install failed")}
	err := Sync(context.Background(), Options{
		ManifestPath: manifestPath,
		StatePath:    statePath,
		Profile:      "desktop",
		Yes:          true,
	}, fake)
	if err == nil || !strings.Contains(err.Error(), "install failed") {
		t.Fatalf("sync error = %v", err)
	}
	if len(fake.remove) == 0 {
		t.Fatal("rollback did not remove partially installed packages")
	}
	if _, err := os.Stat(recovery.Path(statePath)); !os.IsNotExist(err) {
		t.Fatalf("journal error = %v", err)
	}
}

func TestRecoverRollsBackJournalAndClearsIt(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	fake := &fakeBackend{installed: map[string]bool{"git": true}}
	journal := recovery.New(statePath, "server", recovery.Packages{Repository: []string{"git"}}, recovery.Packages{})
	if err := journal.Write(); err != nil {
		t.Fatal(err)
	}
	if err := Recover(context.Background(), Options{StatePath: statePath, Profile: "server", Yes: true}, fake); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.remove, []string{"git"}) {
		t.Fatalf("rollback removals = %#v", fake.remove)
	}
	if _, err := os.Stat(recovery.Path(statePath)); !os.IsNotExist(err) {
		t.Fatalf("journal error = %v", err)
	}
}

func TestBuildAppImagePlanResolvesDeclaredNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packages.yaml")
	contents := []byte(`source: repo
profiles:
  server:
    packages:
      headless:
        tool:
          source: appimage
          address: https://github.com/acme/tool/releases/download/v1.0.0/tool.AppImage
          sha256: ` + strings.Repeat("a", 64) + `
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(filepath.Join(t.TempDir(), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAppImage{artifact: appimage.Artifact{
		Address:   "https://github.com/acme/tool/releases/download/v1.0.0/tool.AppImage",
		Algorithm: "sha256",
		Digest:    strings.Repeat("a", 64),
		Version:   "v1.0.0",
	}}
	plan, err := BuildAppImagePlan(context.Background(), m, s, "server", fake)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Missing, []string{"tool"}) || plan.Records["tool"].Target == "" {
		t.Fatalf("plan = %#v", plan)
	}
	if got := plan.Specs["tool"].Sha256; got != strings.Repeat("a", 64) {
		t.Fatalf("pinned sha256 = %q", got)
	}
}

func TestSyncInstallsAndTracksAppImage(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "packages.yaml")
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	contents := []byte(`source: repo
profiles:
  server:
    packages:
      headless:
        tool:
          source: appimage
          address: https://github.com/acme/tool/releases/download/v1.0.0/tool.AppImage
`)
	if err := os.WriteFile(manifestPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	appSystem := &fakeAppImage{artifact: appimage.Artifact{
		Address:   "https://github.com/acme/tool/releases/download/v1.0.0/tool.AppImage",
		Algorithm: "sha256",
		Digest:    strings.Repeat("b", 64),
		Version:   "v1.0.0",
	}}
	fake := &fakeBackend{installed: map[string]bool{}}
	if err := Sync(context.Background(), Options{
		ManifestPath:   manifestPath,
		StatePath:      statePath,
		Profile:        "server",
		Yes:            true,
		AppImageSystem: appSystem,
	}, fake); err != nil {
		t.Fatal(err)
	}
	if !appSystem.installed["tool"] {
		t.Fatal("AppImage was not installed")
	}
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AppImages()["tool"]; !ok {
		t.Fatalf("AppImages = %#v", s.AppImages())
	}
	if _, err := os.Stat(recovery.Path(statePath)); !os.IsNotExist(err) {
		t.Fatalf("journal error = %v", err)
	}
}

func TestRecoverRemovesPendingAppImage(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	fake := &fakeAppImage{
		artifact:  appimage.Artifact{Address: "https://example.invalid/tool.AppImage", Algorithm: "sha256", Digest: strings.Repeat("c", 64)},
		installed: map[string]bool{"tool": true},
	}
	journal := recovery.New(statePath, "server", recovery.Packages{}, recovery.Packages{})
	journal.AppImages.Installed = []recovery.AppImage{{
		Name: "tool", Address: "https://example.invalid/tool.AppImage", Target: "/tmp/tool", Algorithm: "sha256", Digest: strings.Repeat("c", 64),
	}}
	if err := journal.Write(); err != nil {
		t.Fatal(err)
	}
	if err := Recover(context.Background(), Options{StatePath: statePath, Profile: "server", Yes: true, AppImageSystem: fake}, &fakeBackend{installed: map[string]bool{}}); err != nil {
		t.Fatal(err)
	}
	if fake.installed["tool"] {
		t.Fatal("pending AppImage was not removed")
	}
}

func TestEnsureSelectionsMatchesInteractivePromptsAndConsumesAnswers(t *testing.T) {
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "packages.yaml")
	if err := os.WriteFile(manifestPath, []byte(`source: repo
profiles:
  desktop:
    packages:
      desktop: {}
      extras:
        extra: {}
      hyprland:
        hypr: {}
      i3:
        i3: {}
    options:
      optional:
        default: yes
        prompt: This prompt comes from metadata
        packages:
          optional-package: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(filepath.Join(directory, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	options, err := (Options{
		ManifestPath: manifestPath,
		Profile:      "desktop",
		Input:        strings.NewReader("y\nn\ny\n\n"),
		Output:       &output,
	}).normalize()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := EnsureSelections(m, s, options)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("selections were not changed")
	}
	want := map[string]bool{
		"extras":           true,
		"hyprland":         false,
		"i3":               true,
		"options.optional": true,
	}
	for path, expected := range want {
		parts := strings.Split(path, ".")
		got, recorded := s.Selection(parts...)
		if !recorded || got != expected {
			t.Fatalf("selection %s = %v, recorded = %v", path, got, recorded)
		}
	}
	if !strings.Contains(output.String(), "This prompt comes from metadata") {
		t.Fatal("manifest prompt was not used")
	}
	if !strings.Contains(output.String(), "Optional set optional includes:\n  optional-package\nThis prompt comes from metadata ") {
		t.Fatalf("optional prompt output = %q", output.String())
	}
}

func TestEnsureSelectionsYesSelectsAllMissingSelections(t *testing.T) {
	manifestPath := testManifest(t)
	m, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(filepath.Join(t.TempDir(), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := EnsureSelections(m, s, Options{Profile: "desktop", Yes: true, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("selections were not changed")
	}
	for _, selection := range []string{"extras", "hyprland", "i3"} {
		if selected, recorded := s.Selection(selection); !recorded || !selected {
			t.Fatalf("selection %s = %v, recorded = %v", selection, selected, recorded)
		}
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
	for _, packageName := range append(append([]string{}, repository...), aur...) {
		f.installed[packageName] = true
	}
	return f.installErr
}

func (f *fakeBackend) Remove(_ context.Context, packages []string, profile string) error {
	f.remove = append(f.remove, packages...)
	f.removeProfile = profile
	return nil
}

var _ backend.Backend = (*fakeBackend)(nil)

type fakeAppImage struct {
	artifact  appimage.Artifact
	target    string
	installed map[string]bool
}

func (f *fakeAppImage) Resolve(_ context.Context, _ appimage.Spec) (appimage.Artifact, error) {
	return f.artifact, nil
}

func (f *fakeAppImage) Target(spec appimage.Spec) (string, error) {
	if f.target != "" {
		return f.target, nil
	}
	return filepath.Join("/tmp", spec.Name), nil
}

func (f *fakeAppImage) Installed(_ context.Context, record appimage.Record) (bool, error) {
	return f.installed != nil && f.installed[record.Name], nil
}

func (f *fakeAppImage) Install(_ context.Context, spec appimage.Spec, artifact appimage.Artifact) (appimage.Record, error) {
	if f.installed == nil {
		f.installed = map[string]bool{}
	}
	f.installed[spec.Name] = true
	target, _ := f.Target(spec)
	return appimage.Record{Name: spec.Name, Address: artifact.Address, Target: target, Algorithm: artifact.Algorithm, Digest: artifact.Digest, Version: artifact.Version}, nil
}

func (f *fakeAppImage) Remove(_ context.Context, record appimage.Record) error {
	if f.installed != nil {
		delete(f.installed, record.Name)
	}
	return nil
}

var _ appimage.System = (*fakeAppImage)(nil)

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

func TestDefaultManifestPathUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("DOTPKG_MANIFEST", "/etc/dotpkg/packages.yaml")
	if got := DefaultManifestPath(); got != "/etc/dotpkg/packages.yaml" {
		t.Fatalf("default manifest = %q", got)
	}
	if got, err := (Options{}).normalize(); err != nil || got.ManifestPath != "/etc/dotpkg/packages.yaml" {
		t.Fatalf("normalized manifest = %q, error = %v", got.ManifestPath, err)
	}
}

func TestInitCreatesAndSelectsUserConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("DOTPKG_MANIFEST", "")
	t.Setenv("XDG_CONFIG_HOME", directory)
	var output strings.Builder
	if err := Init(&output); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "dotpkg", "package.yaml")
	statePath := filepath.Join(directory, "dotpkg", "state.yaml")
	if !fileExists(manifestPath) || !fileExists(statePath) {
		t.Fatalf("initialized files missing: %s, %s", manifestPath, statePath)
	}
	if got := DefaultManifestPath(); got != manifestPath {
		t.Fatalf("default manifest = %q, want %q", got, manifestPath)
	}
	options, err := (Options{}).normalize()
	if err != nil {
		t.Fatal(err)
	}
	if options.StatePath != statePath {
		t.Fatalf("default state = %q, want %q", options.StatePath, statePath)
	}
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(original, []byte("# user changes\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(nil); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(updated), "# user changes\n") {
		t.Fatal("repeated init overwrote the user manifest")
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
	if fake.removeProfile != "desktop" {
		t.Fatalf("remove profile = %q", fake.removeProfile)
	}
	loaded, err := state.Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(loaded.Packages(), ","); got != "bat,git" {
		t.Fatalf("managed packages = %q", got)
	}
}

func TestSyncUsesServerRemovalProfile(t *testing.T) {
	manifestPath := testManifest(t)
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPackages([]string{"old-package"})
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBackend{installed: map[string]bool{"git": true}}
	if err := Sync(context.Background(), Options{
		ManifestPath: manifestPath,
		StatePath:    statePath,
		Profile:      "server",
		Yes:          true,
	}, fake); err != nil {
		t.Fatal(err)
	}
	if fake.removeProfile != "server" {
		t.Fatalf("remove profile = %q", fake.removeProfile)
	}
}

func TestSyncDryRunDoesNotWriteOrApply(t *testing.T) {
	manifestPath := testManifest(t)
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.Set(false, "current", "selections", "extras")
	s.Set(false, "current", "selections", "hyprland")
	s.Set(false, "current", "selections", "i3")
	s.SetPackages([]string{"old-package"})
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	fake := &fakeBackend{installed: map[string]bool{"git": true}}
	if err := Sync(context.Background(), Options{
		ManifestPath: manifestPath,
		StatePath:    statePath,
		Profile:      "desktop",
		DryRun:       true,
		Output:       &output,
	}, fake); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("dry-run changed state")
	}
	if len(fake.install) != 0 || len(fake.remove) != 0 {
		t.Fatalf("dry-run applied changes: install=%#v remove=%#v", fake.install, fake.remove)
	}
	if strings.Contains(output.String(), "Apply packages changes") {
		t.Fatal("dry-run prompted for confirmation")
	}
}

func TestSyncDeclinedConfirmationDoesNotWriteState(t *testing.T) {
	manifestPath := testManifest(t)
	statePath := filepath.Join(t.TempDir(), "state.yaml")
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.Set(false, "current", "selections", "extras")
	s.Set(false, "current", "selections", "hyprland")
	s.Set(false, "current", "selections", "i3")
	s.SetPackages([]string{"old-package"})
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeBackend{installed: map[string]bool{"git": true}}
	if err := Sync(context.Background(), Options{
		ManifestPath: manifestPath,
		StatePath:    statePath,
		Profile:      "desktop",
		Input:        strings.NewReader("n\n"),
	}, fake); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("declined sync changed state")
	}
	if len(fake.install) != 0 || len(fake.remove) != 0 {
		t.Fatalf("declined sync applied changes: install=%#v remove=%#v", fake.install, fake.remove)
	}
}
