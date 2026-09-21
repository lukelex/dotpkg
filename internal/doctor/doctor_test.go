package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/reconcile"
	"github.com/lukelex/dotpkg/internal/state"
)

type fakeSystem struct{}

func (fakeSystem) IsInstalled(context.Context, string) (bool, error) { return false, nil }
func (fakeSystem) RepositoryPackages(context.Context) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (fakeSystem) AURPackages(context.Context, []string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}
func (fakeSystem) Install(context.Context, []string, []string, string) error { return nil }
func (fakeSystem) Remove(context.Context, []string, string) error            { return nil }
func (fakeSystem) CurrentGroups(context.Context, string) ([]string, error)   { return nil, nil }
func (fakeSystem) GroupExists(context.Context, string) (bool, error)         { return false, nil }
func (fakeSystem) AddToGroup(context.Context, string, string) error          { return nil }
func (fakeSystem) RemoveFromGroup(context.Context, string, string) error     { return nil }
func (fakeSystem) ServiceExists(context.Context, bool, string) (bool, error) {
	return false, nil
}
func (fakeSystem) ServiceEnabled(context.Context, bool, string) (bool, error) {
	return false, nil
}
func (fakeSystem) ReloadServices(context.Context, bool) error         { return nil }
func (fakeSystem) RestartService(context.Context, bool, string) error { return nil }
func (fakeSystem) EnableService(context.Context, bool, string) error  { return nil }
func (fakeSystem) DisableService(context.Context, bool, string) error { return nil }

func TestRunReportsExecutableLinkAndDirectoryDrift(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "linux", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "linux", "scripts", "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	manifestPath := filepath.Join(root, "packages.yaml")
	contents := `source: repo
resources:
  executable_links:
    - source: linux/scripts
      target: ` + filepath.Join(root, "bin") + `
  directories:
    - $HOME/projects
`
	if err := os.WriteFile(manifestPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(filepath.Join(root, "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), m, s, reconcile.Options{Profile: "server", RootPath: root}, fakeSystem{})
	if !hasFinding(report, "executable_links", "executable link is missing") {
		t.Fatalf("executable link finding missing: %#v", report.Findings)
	}
	if !hasFinding(report, "directories", "declared directory is missing") {
		t.Fatalf("directory finding missing: %#v", report.Findings)
	}
}

func hasFinding(report Report, check, message string) bool {
	for _, finding := range report.Findings {
		if finding.Check == check && len(finding.Message) >= len(message) && finding.Message[:len(message)] == message {
			return true
		}
	}
	return false
}
