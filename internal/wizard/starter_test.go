package wizard

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/reconcile"
)

func starterOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	return Options{Bootstrap: true, Reconcile: reconcile.Options{ManifestPath: reconcile.ConfigManifestPath(), Profile: "desktop"}}
}

func TestStarterPreviewAndPreparationDoNotCreateManifest(t *testing.T) {
	o := starterOptions(t)
	o.Reconcile.DryRun = true
	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	r, choices, err := prepare(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 3 || len(choices[0].Packages) == 0 {
		t.Fatalf("missing starter choices: %+v", choices)
	}
	r.session.Profile = "server"
	choices, err = r.reloadChoices()
	if err != nil || len(choices) != 0 {
		t.Fatalf("server choices: %+v, %v", choices, err)
	}
	for _, path := range []string{reconcile.ConfigDir(), os.Getenv("XDG_STATE_HOME")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preparation wrote %s: %v", path, err)
		}
	}
}

func TestStarterApprovalPersistsUsableManifest(t *testing.T) {
	o := starterOptions(t)
	r, _, err := prepare(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.begin(); err != nil {
		t.Fatal(err)
	}
	defer r.log.Close()
	defer r.events.Close()
	m, err := manifest.Load(o.Reconcile.ManifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(m); err != nil {
		t.Fatal(err)
	}
	if len(m.PackageNames("common.packages.headless")) != 3 {
		t.Fatal("missing base packages")
	}
	cmd := r.command(context.Background(), r.spec.Steps[0].Action)
	for i, arg := range cmd.Args {
		if arg == "--state-file" && cmd.Args[i+1] != reconcile.ConfigStatePath() {
			t.Fatalf("wrong state path: %v", cmd.Args)
		}
	}
	o.Bootstrap = false
	o.Resume = true
	if _, _, err := prepare(o); err != nil {
		t.Fatalf("resume saved starter: %v", err)
	}
}

func TestStarterNeverOverwritesExistingManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package.yaml")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := createStarter(path); err == nil {
		t.Fatal("replaced existing manifest")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing" {
		t.Fatalf("existing data changed: %q, %v", data, err)
	}
}
