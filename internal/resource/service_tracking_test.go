package resource

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

type trackingSystem struct {
	fakeSystem
	active  bool
	fail    bool
	reloads []string
	unit    string
}

func (f *trackingSystem) ServiceActive(context.Context, bool, string) (bool, error) {
	return f.active, nil
}

func (f *trackingSystem) ReloadService(_ context.Context, user bool, name string) error {
	f.reloads = append(f.reloads, serviceKey(user, name))
	if f.fail {
		return errors.New("reload failed")
	}
	return nil
}

func (f *trackingSystem) RestartService(ctx context.Context, user bool, name string) error {
	_ = f.fakeSystem.RestartService(ctx, user, name)
	if f.fail {
		return errors.New("restart failed")
	}
	return nil
}

func (f *trackingSystem) ServiceFiles(context.Context, bool, string) ([]string, error) {
	if f.unit != "" {
		return []string{f.unit}, nil
	}
	return nil, nil
}

func trackingFixture(t *testing.T, action string) (*manifest.Manifest, *state.State, Options, *trackingSystem, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config")
	writeDependency(t, path, "old")
	m := &manifest.Manifest{Path: filepath.Join(root, "packages.yaml"), Data: map[string]any{
		"resources": map[string]any{"services": []any{map[string]any{"name": "tool.service", "scope": "user", "dependencies": []any{map[string]any{"path": "config", "action": action}}}}},
	}}
	s := state.New(filepath.Join(root, "state.yaml"))
	s.SetItems([]string{"user:tool.service"}, state.ManagedKey, state.ManagedServices)
	options := Options{RootPath: root, Profile: "desktop", User: "test", Yes: true, Input: strings.NewReader(""), Output: io.Discard}
	f := &trackingSystem{active: true, fakeSystem: fakeSystem{services: map[string]bool{"user:tool.service": true}, serviceState: map[string]bool{"user:tool.service": true}}}
	if err := Sync(context.Background(), m, s, options, f); err != nil {
		t.Fatal(err)
	}
	if len(f.restarted)+len(f.reloads) != 0 {
		t.Fatal("first observation acted on a service")
	}
	return m, s, options, f, path
}

func writeDependency(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestServiceContentChangePersistsUntilApproved(t *testing.T) {
	m, s, o, f, path := trackingFixture(t, "restart")
	writeDependency(t, path, "new")
	o.Yes = false
	o.Input = strings.NewReader("n\n")
	var output bytes.Buffer
	o.Output = &output
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	if len(f.restarted) != 0 || !strings.Contains(output.String(), "Apply affected service actions?") {
		t.Fatalf("output=%s restarts=%v", output.String(), f.restarted)
	}
	s, err := state.Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(context.Background(), m, s, o, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ServiceActions) != 1 || plan.Changes() != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	o.RestartServices = true
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	if len(f.restarted) != 1 {
		t.Fatalf("restarts=%v", f.restarted)
	}
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	if len(f.restarted) != 1 {
		t.Fatal("restarted unchanged service")
	}
}

func TestServiceRevertCancelsPending(t *testing.T) {
	m, s, o, f, path := trackingFixture(t, "reload")
	writeDependency(t, path, "new")
	o.Yes = false
	o.NonInteractive = true
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	writeDependency(t, path, "old")
	o.Yes = true
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	if len(f.reloads)+len(f.restarted) != 0 {
		t.Fatal("acted on reverted change")
	}
}

func TestServiceReloadFailureStaysPendingWithoutRestart(t *testing.T) {
	m, s, o, f, path := trackingFixture(t, "reload")
	writeDependency(t, path, "new")
	f.fail = true
	err := Sync(context.Background(), m, s, o, f)
	var actionErr *ServiceActionError
	if !errors.As(err, &actionErr) {
		t.Fatalf("error=%v", err)
	}
	if len(f.reloads) != 1 || len(f.restarted) != 0 {
		t.Fatalf("reloads=%v restarts=%v", f.reloads, f.restarted)
	}
	f.fail = false
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	if len(f.reloads) != 2 {
		t.Fatal("failed action was lost")
	}
}

func TestServiceInactiveClearsAndDryRunPreservesBaseline(t *testing.T) {
	m, s, o, f, path := trackingFixture(t, "restart")
	before, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	writeDependency(t, path, "new")
	o.DryRun = true
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || len(f.restarted) != 0 {
		t.Fatal("dry run mutated state or services")
	}
	o.DryRun = false
	f.active = false
	o.Yes = false
	o.NonInteractive = true
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	f.active = true
	o.Yes = true
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	if len(f.restarted) != 0 {
		t.Fatal("inactive service retained action")
	}
}

func TestServicePackageChangeAndUnitChangeOverrideReload(t *testing.T) {
	for _, kind := range []string{"package", "unit"} {
		t.Run(kind, func(t *testing.T) {
			m, s, o, f, path := trackingFixture(t, "reload")
			if kind == "unit" {
				f.unit = filepath.Join(o.RootPath, "tool.service")
				writeDependency(t, f.unit, "old unit")
			}
			services := m.Data["resources"].(map[string]any)["services"].([]any)
			entry := services[0].(map[string]any)
			entry["dependencies"] = append(entry["dependencies"].([]any), map[string]any{"package": "tool"})
			tracker, err := PrepareServices(context.Background(), m, s, o, f)
			if err != nil {
				t.Fatal(err)
			}
			writeDependency(t, path, "new")
			if kind == "package" {
				tracker.PackageChanges([]string{"tool"})
			} else {
				writeDependency(t, f.unit, "new unit")
			}
			if err := tracker.Finish(context.Background(), s, o, f); err != nil {
				t.Fatal(err)
			}
			if len(f.restarted) != 1 || len(f.reloads) != 0 {
				t.Fatalf("restarts=%v reloads=%v", f.restarted, f.reloads)
			}
			if kind == "unit" && len(f.reloaded) != 1 {
				t.Fatal("missing daemon-reload")
			}
		})
	}
}

func TestServiceNoninteractiveDoesNotReadApproval(t *testing.T) {
	m, s, o, f, path := trackingFixture(t, "restart")
	writeDependency(t, path, "new")
	o.Yes = false
	o.NonInteractive = true
	o.Input = strings.NewReader("yes\n")
	if err := Sync(context.Background(), m, s, o, f); err != nil {
		t.Fatal(err)
	}
	if len(f.restarted) != 0 {
		t.Fatal("unattended run accepted stdin as approval")
	}
}

func TestDependencyDirectoryDeletionAndCycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	writeDependency(t, path, "first")
	first, err := fingerprint(dir, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	writeDependency(t, path, "second")
	second, err := fingerprint(dir, map[string]bool{})
	if err != nil || first == second {
		t.Fatalf("digest=%s error=%v", second, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	deleted, err := fingerprint(path, map[string]bool{})
	if err != nil || deleted != "missing" {
		t.Fatalf("digest=%s error=%v", deleted, err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "cycle")); err != nil {
		t.Fatal(err)
	}
	if _, err := fingerprint(dir, map[string]bool{}); err == nil {
		t.Fatal("accepted cycle")
	}
}
