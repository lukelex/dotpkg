package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/resource"
	"github.com/lukelex/dotpkg/internal/state"
)

// Embedding an unimplemented resource interface makes unexpected resource
// reconciliation fail immediately, rather than silently passing this test.
type serviceOnlySystem struct {
	resource.System
	attempts int
}

func (*serviceOnlySystem) ServiceActive(context.Context, bool, string) (bool, error) {
	return true, nil
}

func (s *serviceOnlySystem) RestartService(context.Context, bool, string) error {
	s.attempts++
	return nil
}

func TestDefaultSyncDetectsAndRemembersServiceChanges(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "packages.yaml")
	statePath := filepath.Join(root, "state.yaml")
	dependency := filepath.Join(root, "config")
	contents := `source: repo
profiles:
  server:
    packages:
      headless: {}
resources:
  groups: [unrelated]
  directories: ["$HOME/should-not-be-created"]
  services:
    - name: tool.service
      scope: user
      dependencies:
        - path: config
    - name: unmanaged.service
      scope: user
      dependencies:
        - path: cycle
`
	if err := os.WriteFile(manifestPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependency, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An unreadable dependency of an unmanaged service must not be inspected.
	if err := os.Symlink("cycle", filepath.Join(root, "cycle")); err != nil {
		t.Fatal(err)
	}
	s := state.New(statePath)
	s.SetItems([]string{"user:tool.service"}, state.ManagedKey, state.ManagedServices)
	s.SetItems([]string{"preserved"}, state.ManagedKey, state.ManagedGroups)
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	system := &serviceOnlySystem{}
	packages := &fakeBackend{installed: map[string]bool{}}
	var output bytes.Buffer
	o := Options{ManifestPath: manifestPath, StatePath: statePath, RootPath: root, Profile: "server", ResourceSystem: system, Input: strings.NewReader(""), Output: &output}
	if err := Sync(context.Background(), o, packages); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependency, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	o.Input = strings.NewReader("n\n")
	if err := Sync(context.Background(), o, packages); err != nil {
		t.Fatal(err)
	}
	if system.attempts != 0 || !strings.Contains(output.String(), "Apply affected service actions?") {
		t.Fatalf("attempts=%d output=%s", system.attempts, output.String())
	}
	if strings.Contains(output.String(), "Apply package") {
		t.Fatal("prompted for unchanged packages")
	}

	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	o.DryRun = true
	o.OutputFormat = "json"
	output.Reset()
	if err := Sync(context.Background(), o, packages); err != nil {
		t.Fatal(err)
	}
	var doc PlanDocument
	if err := json.Unmarshal(output.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, output.String())
	}
	found := false
	for _, stage := range doc.Stages {
		if stage.Name == "service_actions" && len(stage.Changes) == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing pending action: %s", output.String())
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("dry run modified state")
	}

	o.DryRun = false
	o.OutputFormat = "text"
	o.Input = strings.NewReader("y\n")
	if err := Sync(context.Background(), o, packages); err != nil {
		t.Fatal(err)
	}
	if system.attempts != 1 {
		t.Fatalf("attempts=%d", system.attempts)
	}
	loaded, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Items(state.ManagedKey, state.ManagedServices); len(got) != 1 || got[0] != "user:tool.service" {
		t.Fatalf("service ownership changed: %v", got)
	}
	if got := loaded.Items(state.ManagedKey, state.ManagedGroups); len(got) != 1 || got[0] != "preserved" {
		t.Fatalf("group ownership changed: %v", got)
	}
}
