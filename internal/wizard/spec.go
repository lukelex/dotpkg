// Package wizard provides manifest-driven installation and onboarding. Checks
// must be read-only; actions run only after the user approves the plan.
package wizard

import (
	"bytes"
	"fmt"
	"regexp"

	"github.com/lukelex/dotpkg/internal/manifest"
	"gopkg.in/yaml.v3"
)

type Spec struct {
	Version         int     `yaml:"version"`
	Title           string  `yaml:"title"`
	Description     string  `yaml:"description"`
	ContinueOnLogin bool    `yaml:"continue_on_login"`
	Inputs          []Input `yaml:"inputs"`
	Steps           []Step  `yaml:"steps"`
}

type Input struct {
	ID             string   `yaml:"id"`
	Title          string   `yaml:"title"`
	Description    string   `yaml:"description"`
	Default        string   `yaml:"default"`
	DefaultCommand []string `yaml:"default_command"`
	Choices        []string `yaml:"choices"`
}

type Step struct {
	ID               string   `yaml:"id"`
	Title            string   `yaml:"title"`
	Description      string   `yaml:"description"`
	Instructions     string   `yaml:"instructions"`
	URL              string   `yaml:"url"`
	Profiles         []string `yaml:"profiles"`
	PackagesOnly     bool     `yaml:"packages_only"`
	DependsOn        []string `yaml:"depends_on"`
	Optional         bool     `yaml:"optional"`
	Graphical        bool     `yaml:"graphical"`
	Check            []string `yaml:"check"`
	Display          []string `yaml:"display"` // public information only; never written to logs
	Action           []string `yaml:"action"`
	UnattendedAction []string `yaml:"unattended_action"`
	Interactive      bool     `yaml:"interactive"`
	LogOutput        bool     `yaml:"log_output"`
	Manual           bool     `yaml:"manual"`
	Kind             string   `yaml:"kind"` // empty (command) or config-conflicts
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_\-]*$`)

func Load(m *manifest.Manifest) (Spec, error) {
	value, ok := m.Data["onboarding"]
	if !ok {
		return Spec{}, fmt.Errorf("manifest has no onboarding section")
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return Spec{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var spec Spec
	if err := decoder.Decode(&spec); err != nil {
		return spec, err
	}
	if spec.Version != 1 {
		return spec, fmt.Errorf("unsupported onboarding version %d", spec.Version)
	}
	if spec.Title == "" || len(spec.Steps) == 0 {
		return spec, fmt.Errorf("onboarding requires title and steps")
	}
	seen := map[string]bool{}
	for _, input := range spec.Inputs {
		if !identifier.MatchString(input.ID) || seen[input.ID] || input.Title == "" {
			return spec, fmt.Errorf("invalid or duplicate input %q", input.ID)
		}
		seen[input.ID] = true
	}
	seen = map[string]bool{}
	for _, step := range spec.Steps {
		if !identifier.MatchString(step.ID) || seen[step.ID] || step.Title == "" {
			return spec, fmt.Errorf("invalid or duplicate step %q", step.ID)
		}
		for _, dependency := range step.DependsOn {
			if !seen[dependency] {
				return spec, fmt.Errorf("step %s depends on unknown or later step %s", step.ID, dependency)
			}
		}
		if step.Kind != "" && step.Kind != "config-conflicts" {
			return spec, fmt.Errorf("unknown step kind %q", step.Kind)
		}
		if len(step.Action) == 0 && len(step.Check) == 0 && !step.Manual && step.Kind == "" {
			return spec, fmt.Errorf("step %s has no check, action or manual confirmation", step.ID)
		}
		for _, profile := range step.Profiles {
			if profile != "desktop" && profile != "server" {
				return spec, fmt.Errorf("unknown profile %q", profile)
			}
		}
		seen[step.ID] = true
	}
	return spec, nil
}

func (s Spec) ForProfile(profile string, packagesOnly bool) []Step {
	var steps []Step
	for _, step := range s.Steps {
		if packagesOnly && !step.PackagesOnly {
			continue
		}
		matches := len(step.Profiles) == 0
		for _, p := range step.Profiles {
			matches = matches || p == profile
		}
		if matches {
			steps = append(steps, step)
		}
	}
	return steps
}
