package reconcile

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

func fixturePath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "testdata", "fixtures"}, parts...)...)
}

func TestCompatibilityFixturesCoverSelectionSets(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "dotfiles-packages.yaml")
	m, err := manifest.Load(manifestPath, "")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name           string
		state          string
		wantPackages   int
		wantSelections bool
	}{
		{name: "selected", state: "selected-state.yaml", wantPackages: 142, wantSelections: true},
		{name: "unselected", state: "unselected-state.yaml", wantPackages: 85, wantSelections: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, err := state.Load(fixturePath(test.state))
			if err != nil {
				t.Fatal(err)
			}
			packages := DeclaredPackages(m, PackageCategories(m, "desktop", true, s))
			if len(packages) != test.wantPackages {
				t.Fatalf("package count = %d, want %d", len(packages), test.wantPackages)
			}
			for _, selection := range []string{"extras", "hyprland", "i3"} {
				selected, recorded := s.Selection(selection)
				if !recorded || selected != test.wantSelections {
					t.Fatalf("selection %s = %v, recorded = %v", selection, selected, recorded)
				}
			}
		})
	}
}

func TestCompatibilityFixtureHostOverlayAddsPackage(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "testdata", "dotfiles-packages.yaml")
	overlayPath := fixturePath("hosts", "laptop.yaml")
	m, err := manifest.Load(manifestPath, overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.PackageOrigin("host-only-tool"); got != "repo" {
		t.Fatalf("host package origin = %q", got)
	}

	s, err := state.Load(fixturePath("selected-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	packages := DeclaredPackages(m, PackageCategories(m, "desktop", true, s))
	if len(packages) != 143 || !containsString(packages, "host-only-tool") {
		t.Fatalf("host package set = %d entries, contains host package: %v", len(packages), containsString(packages, "host-only-tool"))
	}
}

func TestCompatibilityFixtureLegacyStateMigrates(t *testing.T) {
	s, err := state.Load(filepath.Join(fixturePath("legacy"), "state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Packages(), ","); got != "bat,git" {
		t.Fatalf("legacy packages = %q", got)
	}
}

func TestCompatibilityFixtureSharedStatePreservesResourceOwnership(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "state.yaml")
	contents, err := os.ReadFile(fixturePath("shared-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPackages([]string{"bat", "git"})
	if err := s.Write(); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]any{
		"managed.groups":   []any{"docker"},
		"managed.configs":  []any{"linux/config/example:$HOME/.config/example"},
		"managed.services": []any{"docker"},
	} {
		if got, ok := loaded.Get(splitFixturePath(path)...); !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s = %#v, want %#v", path, got, want)
		}
	}
}

func splitFixturePath(path string) []string {
	return strings.Split(path, ".")
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestCompatibilityFixturePlanCanUseSelectedState(t *testing.T) {
	m, err := manifest.Load(filepath.Join("..", "..", "testdata", "dotfiles-packages.yaml"), "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(fixturePath("selected-state.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeBackend{installed: map[string]bool{}}
	plan, err := BuildPlan(context.Background(), m, s, "desktop", fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Missing) != 142 {
		t.Fatalf("missing package count = %d, want 142", len(plan.Missing))
	}
}
