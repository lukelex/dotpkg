package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

type Packages struct {
	Repository []string `yaml:"repository,omitempty" json:"repository,omitempty"`
	AUR        []string `yaml:"aur,omitempty" json:"aur,omitempty"`
	Unknown    []string `yaml:"unknown,omitempty" json:"unknown,omitempty"`
}

type AppImage struct {
	Name      string `yaml:"name" json:"name"`
	Address   string `yaml:"address" json:"address"`
	Target    string `yaml:"target" json:"target"`
	Algorithm string `yaml:"algorithm" json:"algorithm"`
	Digest    string `yaml:"digest" json:"digest"`
	Version   string `yaml:"version,omitempty" json:"version,omitempty"`
}

type AppImages struct {
	Installed []AppImage `yaml:"installed,omitempty" json:"installed,omitempty"`
	Removed   []AppImage `yaml:"removed,omitempty" json:"removed,omitempty"`
}

type Journal struct {
	Version   int       `yaml:"version" json:"version"`
	StartedAt string    `yaml:"started_at" json:"started_at"`
	StatePath string    `yaml:"state_path" json:"state_path"`
	Profile   string    `yaml:"profile" json:"profile"`
	Completed bool      `yaml:"completed,omitempty" json:"completed,omitempty"`
	Installed Packages  `yaml:"installed" json:"installed"`
	Removed   Packages  `yaml:"removed" json:"removed"`
	AppImages AppImages `yaml:"appimages,omitempty" json:"appimages,omitempty"`
}

func Path(statePath string) string { return statePath + ".journal.yaml" }

func New(statePath, profile string, installed, removed Packages) Journal {
	return Journal{
		Version:   CurrentVersion,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		StatePath: statePath,
		Profile:   profile,
		Installed: installed,
		Removed:   removed,
	}
}

func Load(statePath string) (Journal, error) {
	contents, err := os.ReadFile(Path(statePath))
	if os.IsNotExist(err) {
		return Journal{}, os.ErrNotExist
	}
	if err != nil {
		return Journal{}, fmt.Errorf("read recovery journal: %w", err)
	}
	var journal Journal
	if err := yaml.Unmarshal(contents, &journal); err != nil {
		return Journal{}, fmt.Errorf("parse recovery journal: %w", err)
	}
	if journal.Version != CurrentVersion {
		return Journal{}, fmt.Errorf("unsupported recovery journal version %d", journal.Version)
	}
	return journal, nil
}

func (j Journal) Write() error {
	contents, err := yaml.Marshal(j)
	if err != nil {
		return fmt.Errorf("encode recovery journal: %w", err)
	}
	path := Path(j.StatePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create recovery journal directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dotpkg-journal-*")
	if err != nil {
		return fmt.Errorf("create recovery journal: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace recovery journal: %w", err)
	}
	return nil
}

func Clear(statePath string) error {
	if err := os.Remove(Path(statePath)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove recovery journal: %w", err)
	}
	return nil
}
