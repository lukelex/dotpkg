package wizard

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/reconcile"
	"github.com/lukelex/dotpkg/internal/state"
	"gopkg.in/yaml.v3"
)

// The starter contains package choices only: it does not partition disks or
// configure accounts, bootloaders, networking, or display managers.
const starterManifest = `source: repo
common:
  packages:
    headless:
      git: {}
      curl: {}
      nano: {}
profiles:
  server:
    packages:
      headless: {}
  desktop:
    packages:
      desktop: {}
      extras:
        firefox: {}
        thunar: {}
      hyprland:
        hyprland: {}
        kitty: {}
        xdg-desktop-portal-hyprland: {}
      i3:
        i3-wm: {}
        i3status: {}
        xorg-server: {}
        xorg-xinit: {}
        xterm: {}
    options: {}
onboarding:
  version: 1
  title: Set up this Arch Linux machine
  description: Install Git, curl, and nano, plus your chosen desktop tools. Review the choices before installing.
  steps:
    - id: packages
      title: Install selected packages
      description: Save your package choices and reconcile packages with pacman. Desktop session and system configuration remain manual.
      packages_only: true
      interactive: true
      action: ['${binary}', sync, --manifest, '${manifest}', --state-file, '${state}', --profile, '${profile}', --selections, '${selections}', --require-selections, --yes]
      unattended_action: ['${binary}', sync, --manifest, '${manifest}', --state-file, '${state}', --profile, '${profile}', --selections, '${selections}', --require-selections, --yes]
`

func loadManifest(options Options) (*manifest.Manifest, *state.State, reconcile.Options, error) {
	o := options.Reconcile
	if !options.Bootstrap {
		return reconcile.Load(o)
	}
	if o.HostPath != "" {
		return nil, nil, o, fmt.Errorf("a starter setup has no host overlays")
	}
	if o.Profile == "" {
		o.Profile = "desktop"
	}
	if o.Profile != "desktop" && o.Profile != "server" {
		return nil, nil, o, fmt.Errorf("unknown profile: %s", o.Profile)
	}
	var data map[string]any
	if err := yaml.Unmarshal([]byte(starterManifest), &data); err != nil {
		return nil, nil, o, err
	}
	s, err := state.Load(o.StatePath)
	return &manifest.Manifest{Path: o.ManifestPath, Data: data}, s, o, err
}

// Publish a complete file atomically without replacing a concurrently created
// manifest (including a symlink). Only called after approval.
func createStarter(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".starter-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(starterManifest); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		return fmt.Errorf("create starter manifest without overwriting existing data: %w", err)
	}
	return nil
}
