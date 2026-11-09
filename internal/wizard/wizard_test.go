package wizard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/reconcile"
)

func fixture(t *testing.T, body string) Options {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	path := filepath.Join(root, "packages.yaml")
	if err := os.WriteFile(path, []byte("onboarding:\n  version: 1\n  title: Test installer\n"+body), 0600); err != nil {
		t.Fatal(err)
	}
	return Options{Reconcile: reconcile.Options{ManifestPath: path, RootPath: root, Profile: "server", StatePath: filepath.Join(root, "packages-state.yaml")}}
}

func TestPreviewNeverExecutesCommandsOrWritesState(t *testing.T) {
	options := fixture(t, "  inputs:\n    - id: example\n      title: Example\n      default_command: [sh, -c, 'touch default-ran']\n  steps:\n    - id: setup\n      title: Setup\n      check: [sh, -c, 'touch check-ran']\n      action: [sh, -c, 'touch action-ran']\n")
	options.Reconcile.DryRun = true
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(options.Reconcile.RootPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "packages.yaml" {
		t.Fatalf("preview wrote files: %v", entries)
	}
}

func TestPlainRejectsPromptsBeforeAnyAction(t *testing.T) {
	options := fixture(t, "  steps:\n    - id: earlier\n      title: Earlier\n      action: [sh, -c, 'touch action-ran']\n    - id: prompt\n      title: Password\n      interactive: true\n      action: [sh, -c, 'read answer']\n")
	options.Plain = true
	options.Yes = true
	err := Run(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "interactive wizard") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(options.Reconcile.RootPath, "action-ran")); !os.IsNotExist(err) {
		t.Fatal("ran action before discovering prompt")
	}
}

func TestUnattendedActionAndVerification(t *testing.T) {
	options := fixture(t, "  steps:\n    - id: setup\n      title: Setup\n      check: [test, -f, ready]\n      interactive: true\n      action: [sh, -c, 'exit 99']\n      unattended_action: [touch, ready]\n")
	options.Plain = true
	options.Yes = true
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(options.Reconcile.RootPath, "ready")); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRechecksInsteadOfTrustingPreviousSuccess(t *testing.T) {
	options := fixture(t, "  steps:\n    - id: setup\n      title: Setup\n      check: [test, -f, ready]\n      action: [touch, ready]\n")
	options.Plain = true
	options.Yes = true
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(options.Reconcile.RootPath, "ready")); err != nil {
		t.Fatal(err)
	}
	options.Resume = true
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(options.Reconcile.RootPath, "ready")); err != nil {
		t.Fatal("resume skipped drifted setup")
	}
}

func TestDependencyCannotBeDeferredOrBypassed(t *testing.T) {
	r := &runtime{session: Session{Deferred: map[string]bool{}, Confirmed: map[string]bool{}}}
	m := newModel(r, nil)
	m.page = "run"
	m.steps = []Step{{ID: "ssh", Optional: true}, {ID: "fonts", DependsOn: []string{"ssh"}, Action: []string{"false"}}}
	if m.canDefer(m.steps[0]) {
		t.Fatal("required dependency is deferrable")
	}
	m.index = 1
	m.statuses["ssh"] = "deferred"
	if cmd := m.action(); cmd != nil {
		t.Fatal("ran action with unresolved dependency")
	}
	if m.busy {
		t.Fatal("blocked operation marked running")
	}
}

func TestCancelWaitsForOperationBoundary(t *testing.T) {
	m := newModel(&runtime{}, nil)
	m.page = "run"
	m.busy = true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || !m.stop {
		t.Fatal("cancel interrupted active operation")
	}
	_, cmd = m.Update(actionMsg{})
	if cmd == nil {
		t.Fatal("did not quit at operation boundary")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected quit")
	}
}

func TestLoginContinuationAndDeferralPersistence(t *testing.T) {
	options := fixture(t, "  continue_on_login: true\n  steps:\n    - id: account\n      title: Account\n      optional: true\n      manual: true\n")
	options.Reconcile.Profile = "desktop"
	r, _, err := prepare(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.begin(); err != nil {
		t.Fatal(err)
	}
	defer r.log.Close()
	defer r.events.Close()
	paths, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "autostart", "*.desktop"))
	if len(paths) != 1 {
		t.Fatalf("autostart = %v", paths)
	}
	data, _ := os.ReadFile(paths[0])
	if !strings.Contains(string(data), "Terminal=true") || !strings.Contains(string(data), "--resume") {
		t.Fatal(string(data))
	}
	r.session.Deferred["account"] = true
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	var saved Session
	data, _ = os.ReadFile(r.sessionPath())
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Deferred["account"] {
		t.Fatal("lost deferral")
	}
	if err := r.continuation(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatal("completed wizard left login trigger")
	}
}

func TestSpecRejectsUnknownFieldsAndForwardDependencies(t *testing.T) {
	for _, body := range []string{
		"  typo: true\n  steps: [{id: x, title: X, manual: true}]\n",
		"  steps: [{id: x, title: X, manual: true, depends_on: [later]}]\n",
	} {
		options := fixture(t, body)
		m, err := manifest.Load(options.Reconcile.ManifestPath, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Load(m); err == nil {
			t.Fatal("invalid onboarding accepted")
		}
	}
}
