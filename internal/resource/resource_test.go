package resource

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

type fakeSystem struct {
	groups        []string
	groupExists   map[string]bool
	services      map[string]bool
	serviceState  map[string]bool
	addedGroups   []string
	removedGroups []string
	enabled       []string
	disabled      []string
	reloaded      []string
	restarted     []string
}

func (f *fakeSystem) CurrentGroups(context.Context, string) ([]string, error) { return f.groups, nil }
func (f *fakeSystem) GroupExists(_ context.Context, name string) (bool, error) {
	return f.groupExists[name], nil
}
func (f *fakeSystem) AddToGroup(_ context.Context, _, group string) error {
	f.addedGroups = append(f.addedGroups, group)
	return nil
}
func (f *fakeSystem) RemoveFromGroup(_ context.Context, _, group string) error {
	f.removedGroups = append(f.removedGroups, group)
	return nil
}
func (f *fakeSystem) ServiceExists(_ context.Context, user bool, name string) (bool, error) {
	return f.services[serviceKey(user, name)], nil
}
func (f *fakeSystem) ServiceEnabled(_ context.Context, user bool, name string) (bool, error) {
	return f.serviceState[serviceKey(user, name)], nil
}
func (f *fakeSystem) EnableService(_ context.Context, user bool, name string) error {
	f.enabled = append(f.enabled, serviceKey(user, name))
	return nil
}
func (f *fakeSystem) DisableService(_ context.Context, user bool, name string) error {
	f.disabled = append(f.disabled, serviceKey(user, name))
	return nil
}
func (f *fakeSystem) ReloadServices(_ context.Context, user bool) error {
	f.reloaded = append(f.reloaded, serviceKey(user, "daemon-reload"))
	return nil
}
func (f *fakeSystem) RestartService(_ context.Context, user bool, name string) error {
	f.restarted = append(f.restarted, serviceKey(user, name))
	return nil
}

func serviceKey(user bool, name string) string {
	if user {
		return "user:" + name
	}
	return name
}

func resourceFixture(t *testing.T) (*manifest.Manifest, *state.State, Options, *fakeSystem) {
	t.Helper()
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "packages.yaml")
	contents := `source: repo
common:
  packages:
    headless:
      docker:
        groups: [docker]
        configs: [config/docker:$XDG_CONFIG_HOME/docker]
        services:
          system: [docker]
profiles:
  desktop:
    packages:
      desktop:
        desktop-tool:
          groups: [video]
          services:
            user: [desktop.service]
      dotfiles: {}
      extras: {}
      hyprland: {}
      i3: {}
    options: {}
resources:
  configs:
    - source: config/custom
      target: $XDG_CONFIG_HOME/custom
      profiles: [desktop]
      selections: [i3]
  services:
    - name: custom.service
      scope: user
      profiles: [desktop]
      selections: [i3]
`
	if err := os.WriteFile(manifestPath, []byte(contents), 0o644); err != nil {
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
	for _, selection := range []string{"extras", "hyprland", "i3"} {
		s.Set(false, "current", "selections", selection)
	}
	configHome := filepath.Join(directory, "xdg-config")
	if err := os.MkdirAll(filepath.Join(directory, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config", "docker"), []byte("docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config", "custom"), []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	system := &fakeSystem{
		groups:       []string{"docker"},
		groupExists:  map[string]bool{"docker": true, "video": true},
		services:     map[string]bool{"docker": true, "user:desktop.service": true},
		serviceState: map[string]bool{"docker": true, "user:desktop.service": false},
	}
	options := Options{Profile: "desktop", RootPath: directory, User: "test-user"}
	return m, s, options, system
}

func TestBuildPlanCoversGroupsConfigsAndServices(t *testing.T) {
	m, s, options, system := resourceFixture(t)
	plan, err := BuildPlan(context.Background(), m, s, options, system)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Groups.Declared, []string{"docker", "video"}) {
		t.Fatalf("groups declared = %#v", plan.Groups.Declared)
	}
	if !reflect.DeepEqual(plan.Groups.Adopted, []string{"docker"}) || !reflect.DeepEqual(plan.Groups.Missing, []string{"video"}) {
		t.Fatalf("groups plan = %#v", plan.Groups)
	}
	if !reflect.DeepEqual(plan.Configs.Missing, []string{"config/docker:$XDG_CONFIG_HOME/docker"}) {
		t.Fatalf("configs plan = %#v", plan.Configs)
	}
	if !reflect.DeepEqual(plan.Services.Adopted, []string{"docker"}) || !reflect.DeepEqual(plan.Services.Missing, []string{"user:desktop.service"}) {
		t.Fatalf("services plan = %#v", plan.Services)
	}
	var names []string
	for _, stage := range plan.Stages() {
		names = append(names, stage.Name)
	}
	if !reflect.DeepEqual(names, []string{"groups", "configs", "services", "executable_links", "directories"}) {
		t.Fatalf("resource stage order = %#v", names)
	}
}

func TestExecutableLinkCollectionLinksExecutablesAndPrunesDanglingPrefixLinks(t *testing.T) {
	directory := t.TempDir()
	sourceDir := filepath.Join(directory, "linux", "scripts")
	targetDir := filepath.Join(directory, "bin")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "tool")
	if err := os.WriteFile(source, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(targetDir, "u_old")); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "packages.yaml")
	contents := "source: repo\nresources:\n  executable_links:\n    - source: linux/scripts\n      target: " + targetDir + "\n      prefix: u_\n      prune: true\n"
	if err := os.WriteFile(manifestPath, []byte(contents), 0o644); err != nil {
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
	options := Options{Profile: "server", RootPath: directory, Yes: true}
	system := &fakeSystem{groupExists: map[string]bool{}, services: map[string]bool{}, serviceState: map[string]bool{}}
	plan, err := BuildPlan(context.Background(), m, s, options, system)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(targetDir, "u_tool"), filepath.Join(targetDir, "u_old")}
	if !reflect.DeepEqual(plan.ExecutableLinks.Extra, []string{want[1]}) || !reflect.DeepEqual(plan.ExecutableLinks.Missing, []string{want[0]}) {
		t.Fatalf("executable link plan = %#v", plan.ExecutableLinks)
	}
	if err := Sync(context.Background(), m, s, options, system); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(want[0]); err != nil || got != source {
		t.Fatalf("executable link = %q, error = %v", got, err)
	}
	if _, err := os.Lstat(want[1]); !os.IsNotExist(err) {
		t.Fatalf("stale executable link still exists: %v", err)
	}
	if got := s.Items("managed", "executable_links"); !reflect.DeepEqual(got, []string{want[0]}) {
		t.Fatalf("managed executable links = %#v", got)
	}
}

func TestDirectoryResourcesCreateAdoptAndCleanEmptyDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	manifestPath := filepath.Join(home, "packages.yaml")
	if err := os.WriteFile(manifestPath, []byte(`source: repo
resources:
  directories:
    - $HOME/.ssh
    - path: $HOME/projects
      profiles: [desktop]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(filepath.Join(home, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	system := &fakeSystem{groupExists: map[string]bool{}, services: map[string]bool{}, serviceState: map[string]bool{}}
	options := Options{Profile: "desktop", RootPath: home, Yes: true}
	if err := Sync(context.Background(), m, s, options, system); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, "projects")} {
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			t.Fatalf("directory %s: info=%v error=%v", directory, info, err)
		}
	}
	emptyManifestPath := filepath.Join(home, "empty.yaml")
	if err := os.WriteFile(emptyManifestPath, []byte("source: repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyManifest, err := manifest.Load(emptyManifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Clean(context.Background(), emptyManifest, s, options, system); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, "projects")} {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatalf("directory %s still exists: %v", directory, err)
		}
	}
}

func TestSyncAppliesResourcesAndTracksOwnership(t *testing.T) {
	m, s, options, system := resourceFixture(t)
	options.Yes = true
	if err := Sync(context.Background(), m, s, options, system); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(system.addedGroups, []string{"video"}) {
		t.Fatalf("added groups = %#v", system.addedGroups)
	}
	if !reflect.DeepEqual(system.enabled, []string{"user:desktop.service"}) {
		t.Fatalf("enabled services = %#v", system.enabled)
	}
	target := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "docker")
	if source, err := os.Readlink(target); err != nil || source != filepath.Join(options.RootPath, "config/docker") {
		t.Fatalf("config link = %q, error = %v", source, err)
	}
	if got := strings.Join(s.Items("managed", "groups"), ","); got != "docker,video" {
		t.Fatalf("tracked groups = %q", got)
	}
	if got := strings.Join(s.Items("managed", "services"), ","); got != "docker,user:desktop.service" {
		t.Fatalf("tracked services = %q", got)
	}
}

func TestSyncDoesNotTrackUnavailableResources(t *testing.T) {
	m, s, options, system := resourceFixture(t)
	delete(system.groupExists, "video")
	delete(system.services, "user:desktop.service")
	options.Yes = true
	if err := Sync(context.Background(), m, s, options, system); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Items("managed", "groups"), ","); got != "docker" {
		t.Fatalf("tracked groups = %q", got)
	}
	if got := strings.Join(s.Items("managed", "services"), ","); got != "docker" {
		t.Fatalf("tracked services = %q", got)
	}
}

func TestCurrentManifestResourceSetsMatchBashFixtures(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "dotfiles-packages.yaml")
	m, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		state string
	}{
		{name: "selected", state: "selected-state.yaml"},
		{name: "unselected", state: "unselected-state.yaml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := state.Load(filepath.Join("..", "..", "testdata", "fixtures", test.state))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := declaredMetadata(m, s, "desktop", "groups"), readResourceFixture(t, "expected-desktop-"+test.name+"-groups.txt"); !reflect.DeepEqual(got, want) {
				t.Fatalf("groups = %#v, want %#v", got, want)
			}
			if got, want := declaredMetadata(m, s, "desktop", "configs"), readResourceFixture(t, "expected-desktop-"+test.name+"-configs.txt"); !reflect.DeepEqual(got, want) {
				t.Fatalf("configs = %#v, want %#v", got, want)
			}
			if got, want := declaredServices(m, s, "desktop"), readResourceFixture(t, "expected-desktop-"+test.name+"-services.txt"); !reflect.DeepEqual(got, want) {
				t.Fatalf("services = %#v, want %#v", got, want)
			}
		})
	}

	s, err := state.Load(filepath.Join("..", "..", "testdata", "fixtures", "selected-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"groups", "configs"} {
		if got, want := declaredMetadata(m, s, "server", key), readResourceFixture(t, "expected-server-"+key+".txt"); !reflect.DeepEqual(got, want) {
			t.Fatalf("server %s = %#v, want %#v", key, got, want)
		}
	}
	if got, want := declaredServices(m, s, "server"), readResourceFixture(t, "expected-server-services.txt"); !reflect.DeepEqual(got, want) {
		t.Fatalf("server services = %#v, want %#v", got, want)
	}
}

func TestSyncRemovesOnlyTrackedResourcesAndPreservesOtherState(t *testing.T) {
	m, s, options, system := resourceFixture(t)
	configMapping := "config/docker:$XDG_CONFIG_HOME/docker"
	configTarget := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "docker")
	configSource := filepath.Join(options.RootPath, "config/docker")
	if err := os.MkdirAll(filepath.Dir(configTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(configSource, configTarget); err != nil {
		t.Fatal(err)
	}

	s.SetItems([]string{"docker", "video"}, "managed", "groups")
	s.SetItems([]string{configMapping}, "managed", "configs")
	s.SetItems([]string{"docker", "user:desktop.service"}, "managed", "services")
	s.SetItems([]string{"git"}, "managed", "packages")
	s.Set("keep-me", "current", "unrelated")
	s.Set("also-keep-me", "managed", "unrelated")
	system.serviceState["user:desktop.service"] = true

	common := m.Data["common"].(map[string]any)
	packages := common["packages"].(map[string]any)
	headless := packages["headless"].(map[string]any)
	delete(headless, "docker")

	options.Yes = true
	if err := Sync(context.Background(), m, s, options, system); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(system.removedGroups, []string{"docker"}) {
		t.Fatalf("removed groups = %#v", system.removedGroups)
	}
	if !reflect.DeepEqual(system.disabled, []string{"docker"}) {
		t.Fatalf("disabled services = %#v", system.disabled)
	}
	if _, err := os.Lstat(configTarget); !os.IsNotExist(err) {
		t.Fatalf("config target error = %v, want removed", err)
	}

	loaded, err := state.Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(loaded.Items("managed", "groups"), ","); got != "video" {
		t.Fatalf("remaining groups = %q", got)
	}
	if got := strings.Join(loaded.Items("managed", "configs"), ","); got != "" {
		t.Fatalf("remaining configs = %q", got)
	}
	if got := strings.Join(loaded.Items("managed", "services"), ","); got != "user:desktop.service" {
		t.Fatalf("remaining services = %q", got)
	}
	if got := strings.Join(loaded.Packages(), ","); got != "git" {
		t.Fatalf("packages = %q", got)
	}
	if got, _ := loaded.Get("current", "unrelated"); got != "keep-me" {
		t.Fatalf("current unrelated state = %#v", got)
	}
	if got, _ := loaded.Get("managed", "unrelated"); got != "also-keep-me" {
		t.Fatalf("managed unrelated state = %#v", got)
	}
}

func TestConfigConflictRequiresReplace(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	target := filepath.Join(directory, "config", "target")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := linkConfig(source, target, false); err == nil {
		t.Fatal("linkConfig without replace succeeded for an existing file")
	}
	if err := linkConfig(source, target, true); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(target); err != nil || got != source {
		t.Fatalf("replaced config link = %q, error = %v", got, err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linkConfig(source, target, false); err == nil {
		t.Fatal("linkConfig without replace succeeded for an existing directory")
	}
	if err := linkConfig(source, target, true); err == nil {
		t.Fatal("linkConfig removed an existing directory")
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("directory target changed: info=%v error=%v", info, err)
	}
}

func TestSyncSkipsConfigConflictWithoutTrackingIt(t *testing.T) {
	m, s, options, system := resourceFixture(t)
	target := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "docker")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	options.Yes = true
	if err := Sync(context.Background(), m, s, options, system); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != "user data" {
		t.Fatalf("conflicting config contents = %q, error = %v", contents, err)
	}
	if got := s.Items("managed", "configs"); len(got) != 0 {
		t.Fatalf("tracked conflicting configs = %#v", got)
	}
}

func TestConfigAdoptionMatchesBashStateBoundary(t *testing.T) {
	m, s, options, system := resourceFixture(t)
	target := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "docker")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(options.RootPath, "config/docker"), target); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(context.Background(), m, s, options, system)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Configs.Adopted, []string{"config/docker:$XDG_CONFIG_HOME/docker"}) {
		t.Fatalf("new-state config adoption = %#v", plan.Configs.Adopted)
	}

	s.Exists = true
	plan, err = BuildPlan(context.Background(), m, s, options, system)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Configs.Adopted) != 0 {
		t.Fatalf("existing-state config adoption = %#v", plan.Configs.Adopted)
	}
}

func TestCustomResourcesFollowProfileSelections(t *testing.T) {
	m, s, options, system := resourceFixture(t)
	s.Set(true, "current", "selections", "i3")
	system.services["user:custom.service"] = true
	system.serviceState["user:custom.service"] = false

	plan, err := BuildPlan(context.Background(), m, s, options, system)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(plan.Configs.Declared, "config/custom:$XDG_CONFIG_HOME/custom") {
		t.Fatalf("custom configs = %#v", plan.Configs.Declared)
	}
	if !contains(plan.Services.Declared, "user:custom.service") {
		t.Fatalf("custom services = %#v", plan.Services.Declared)
	}
	if !contains(plan.Services.Missing, "user:custom.service") {
		t.Fatalf("custom service changes = %#v", plan.Services)
	}

	s.Set(false, "current", "selections", "i3")
	plan, err = BuildPlan(context.Background(), m, s, options, system)
	if err != nil {
		t.Fatal(err)
	}
	if contains(plan.Configs.Declared, "config/custom:$XDG_CONFIG_HOME/custom") || contains(plan.Services.Declared, "user:custom.service") {
		t.Fatalf("unselected custom resources remained: configs=%#v services=%#v", plan.Configs.Declared, plan.Services.Declared)
	}
}

func TestConfigProvidedServiceIsReloadedAndEnabled(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "linux", "config", "tool.service")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("[Unit]\nDescription=tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "linux", "packages.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := `source: repo
profiles:
  desktop:
    packages:
      desktop:
        tool:
          configs:
            - linux/config/tool.service:$XDG_CONFIG_HOME/systemd/user/tool.service
          services:
            user:
              - tool.service
      dotfiles: {}
      extras: {}
      hyprland: {}
      i3: {}
    options: {}
`
	if err := os.WriteFile(manifestPath, []byte(contents), 0o644); err != nil {
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
	for _, selection := range []string{"extras", "hyprland", "i3"} {
		s.Set(false, "current", "selections", selection)
	}
	configHome := filepath.Join(directory, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	system := &fakeSystem{
		services:     map[string]bool{},
		serviceState: map[string]bool{},
	}
	options := Options{Profile: "desktop", RootPath: directory, User: "test-user", Yes: true, RestartServices: true}
	plan, err := BuildPlan(context.Background(), m, s, options, system)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(plan.Services.Missing, "user:tool.service") {
		t.Fatalf("service plan = %#v", plan.Services)
	}
	if err := Sync(context.Background(), m, s, options, system); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(system.reloaded, []string{"user:daemon-reload"}) {
		t.Fatalf("reloaded services = %#v", system.reloaded)
	}
	if !reflect.DeepEqual(system.enabled, []string{"user:tool.service"}) {
		t.Fatalf("enabled services = %#v", system.enabled)
	}
	if !reflect.DeepEqual(system.restarted, []string{"user:tool.service"}) {
		t.Fatalf("restarted services = %#v", system.restarted)
	}
}

func TestConfigPathsRejectSourceOutsideRoot(t *testing.T) {
	_, _, err := configPaths("../outside:$HOME/.config/example", Options{RootPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigPathsRejectRelativeTarget(t *testing.T) {
	_, _, err := configPaths("config/example:relative/path", Options{RootPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "target must be absolute") {
		t.Fatalf("error = %v", err)
	}
}

func TestUserDirectoryPathExpandsTildeInsideHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := userDirectoryPath("~/.cache/tool")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".cache", "tool"); path != want {
		t.Fatalf("directory path = %q, want %q", path, want)
	}
}

func TestConfigPathsRejectTargetOutsideHome(t *testing.T) {
	_, _, err := configPaths("config/example:/etc/example", Options{RootPath: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "outside allowed home paths") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigPathsRejectsEscapingSourceSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "tool"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "tool"), filepath.Join(root, "config", "tool")); err != nil {
		t.Fatal(err)
	}
	_, _, err := configPaths("config/tool:$HOME/tool", Options{RootPath: root})
	if err == nil || !strings.Contains(err.Error(), "source symlink escapes root") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigPathsRejectsEscapingTargetParentSymlink(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "links")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	_, _, err := configPaths("config/tool:$HOME/links/tool", Options{RootPath: root})
	if err == nil || !strings.Contains(err.Error(), "target parent symlink escapes") {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigConflictDoesNotRemoveDirectory(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linkConfig(source, target, true); err == nil {
		t.Fatal("linkConfig removed a directory target")
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("target directory was changed: info=%v error=%v", info, err)
	}
}

func TestInspectConfigDetectsBrokenSourceSymlink(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "config", "tool")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-target", source); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "tool")
	if err := os.Symlink(source, target); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Dir(target))
	status, err := InspectConfig("config/tool:$HOME/tool", Options{RootPath: directory})
	if err != nil {
		t.Fatal(err)
	}
	if status.SourceExists {
		t.Fatal("broken source symlink was reported as valid")
	}
	if !status.TargetExists || !status.Matches {
		t.Fatalf("target status = %#v", status)
	}
}

func TestCleanRemovesOnlyStaleManagedResources(t *testing.T) {
	m, s, options, system := resourceFixture(t)
	staleSource := filepath.Join(options.RootPath, "config", "stale")
	staleTarget := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "stale")
	if err := os.WriteFile(staleSource, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(staleTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(staleSource, staleTarget); err != nil {
		t.Fatal(err)
	}
	s.SetItems([]string{"old-group"}, "managed", "groups")
	s.SetItems([]string{"config/stale:$XDG_CONFIG_HOME/stale"}, "managed", "configs")
	s.SetItems([]string{"old.service"}, "managed", "services")
	s.SetItems([]string{"git"}, "managed", "packages")
	system.groups = []string{"old-group"}
	system.groupExists["old-group"] = true
	system.services["old.service"] = true
	options.Yes = true
	if err := Clean(context.Background(), m, s, options, system); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(staleTarget); !os.IsNotExist(err) {
		t.Fatalf("stale config error = %v", err)
	}
	if !reflect.DeepEqual(system.removedGroups, []string{"old-group"}) {
		t.Fatalf("removed groups = %#v", system.removedGroups)
	}
	if !reflect.DeepEqual(system.disabled, []string{"old.service"}) {
		t.Fatalf("disabled services = %#v", system.disabled)
	}
	if len(s.Items("managed", "configs")) != 0 || len(s.Items("managed", "services")) != 0 {
		t.Fatalf("stale resources remain: configs=%#v services=%#v", s.Items("managed", "configs"), s.Items("managed", "services"))
	}
}

func readResourceFixture(t *testing.T, name string) []string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.TrimSpace(string(contents)))
	if len(contents) == 0 {
		return nil
	}
	return strings.Split(string(contents), "\n")
}
