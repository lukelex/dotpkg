package reconcile

import (
	"fmt"
	"strings"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

// SelectionChoice is the supported UI representation of a package choice.
type SelectionChoice struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Packages []string `json:"packages"`
	Selected bool     `json:"selected"`
	Recorded bool     `json:"recorded"`
}

// SelectionChoices returns the available package selection choices.
func SelectionChoices(m *manifest.Manifest, s *state.State, profile string) []SelectionChoice {
	if profile != "desktop" {
		return nil
	}
	choices := []SelectionChoice{}
	for _, item := range []struct{ key, label string }{{"extras", "Desktop extras"}, {"hyprland", "Hyprland"}, {"i3", "i3 / X11"}} {
		value, recorded := s.Selection(item.key)
		choices = append(choices, SelectionChoice{item.key, item.label, m.PackageNames("profiles.desktop.packages." + item.key), value, recorded})
	}
	for _, name := range m.OptionNames() {
		value, recorded := s.Selection("options", name)
		if !recorded {
			value = m.OptionDefault(name)
		}
		choices = append(choices, SelectionChoice{"options." + name, name, m.OptionPackages(name), value, recorded})
	}
	return choices
}

func applySelections(m *manifest.Manifest, s *state.State, options Options) (bool, error) {
	valid := map[string]bool{}
	for _, choice := range SelectionChoices(m, s, options.Profile) {
		valid[choice.Key] = true
	}
	for key := range options.Selections {
		if !valid[key] {
			return false, fmt.Errorf("unknown selection %q for profile %s", key, options.Profile)
		}
	}
	changed := false
	for key, value := range options.Selections {
		path := strings.Split(key, ".")
		previous, recorded := s.Selection(path...)
		if !recorded || previous != value {
			s.Set(value, append([]string{"current", "selections"}, path...)...)
			changed = true
		}
	}
	if options.RequireSelections {
		for _, choice := range SelectionChoices(m, s, options.Profile) {
			if !choice.Recorded {
				return false, fmt.Errorf("selection %s is unresolved; provide --selections or use the wizard", choice.Key)
			}
		}
	}
	return changed, nil
}
