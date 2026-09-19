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
