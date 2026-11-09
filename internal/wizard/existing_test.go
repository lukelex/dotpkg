package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/reconcile"
)

func TestExistingManifestAutoResumeReconcilesEveryRun(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	path := filepath.Join(root, "packages.yaml")
	contents := "source: repo\ncommon:\n  packages:\n    headless:\n      git: {}\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	o := Options{AutoResume: true, Plain: true, Yes: true, Reconcile: reconcile.Options{ManifestPath: path, Profile: "desktop"}}
	r, _, err := prepare(o)
	if err != nil {
		t.Fatal(err)
	}
	if r.options.Resume {
		t.Fatal("resumed nonexistent session")
	}
	wantState := filepath.Join(root, "state", "dotpkg", "state.yaml")
	if r.options.Reconcile.StatePath != wantState {
		t.Fatalf("state path = %s, want %s", r.options.Reconcile.StatePath, wantState)
	}
	r.session.Profile = "server"
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	// Substitute the child executable, so this exercises orchestration without
	// installing/removing anything or querying a live package manager.
	fake := filepath.Join(root, "fake-dotpkg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> calls\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r, choices, err := prepare(o)
		if err != nil {
			t.Fatal(err)
		}
		if !r.options.Resume || r.session.Profile != "server" || len(choices) != 0 {
			t.Fatalf("did not restore session: %+v", r.session)
		}
		if newModel(r, choices).page != "review" {
			t.Fatal("resume did not start at approval")
		}
		r.executable = fake
		if err := r.runPlain(choices); err != nil {
			t.Fatal(err)
		}
		r.log.Close()
		r.events.Close()
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	if err != nil || strings.Count(string(calls), "sync\n") != 2 || !strings.Contains(string(calls), "--state-file\n"+wantState+"\n") {
		t.Fatalf("expected repeated reconciliation: %s, %v", calls, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != contents {
		t.Fatalf("existing manifest modified: %s, %v", after, err)
	}
	o.AutoResume = false
	r, _, err = prepare(o)
	if err != nil || r.session.Profile != "desktop" {
		t.Fatalf("explicit profile not respected: %v", err)
	}
}
