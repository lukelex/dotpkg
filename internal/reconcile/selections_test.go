package reconcile

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

func TestExplicitSelectionsOverrideWithoutWritingDuringPreview(t *testing.T) {
	m := &manifest.Manifest{Data: map[string]any{}}
	s := state.New(filepath.Join(t.TempDir(), "state.yaml"))
	s.Set(true, "current", "selections", "extras")
	changed, err := EnsureSelections(m, s, Options{Profile: "desktop", DryRun: true, Yes: true, RequireSelections: true, Output: io.Discard, Selections: map[string]bool{"extras": false, "hyprland": true, "i3": false}})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if selected, _ := s.Selection("extras"); selected {
		t.Fatal("--yes overrode explicit false")
	}
	if s.Exists {
		t.Fatal("preview wrote state")
	}
}

func TestRequireSelectionsNeverUsesYesForMissingChoice(t *testing.T) {
	m := &manifest.Manifest{Data: map[string]any{}}
	s := state.New(filepath.Join(t.TempDir(), "state.yaml"))
	_, err := EnsureSelections(m, s, Options{Profile: "desktop", Yes: true, RequireSelections: true, Output: io.Discard})
	if err == nil {
		t.Fatal("unattended selection defaulted to yes")
	}
}

func TestUnknownSelectionRejectedBeforeApplyingAny(t *testing.T) {
	m := &manifest.Manifest{Data: map[string]any{}}
	s := state.New(filepath.Join(t.TempDir(), "state.yaml"))
	_, err := EnsureSelections(m, s, Options{Profile: "desktop", Selections: map[string]bool{"extras": true, "typo": true}})
	if err == nil {
		t.Fatal("unknown selection accepted")
	}
	if _, recorded := s.Selection("extras"); recorded {
		t.Fatal("partially applied invalid choices")
	}
}
