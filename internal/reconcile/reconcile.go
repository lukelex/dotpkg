package reconcile

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lukelex/dotpkg/internal/backend"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/resource"
	"github.com/lukelex/dotpkg/internal/state"
)

type Options struct {
	ManifestPath   string
	HostPath       string
	HostLabel      string
	StatePath      string
	Profile        string
	DryRun         bool
	Yes            bool
	Resources      bool
	RootPath       string
	Replace        bool
	ResourceSystem resource.System
	Input          io.Reader
	Output         io.Writer
}

type Plan struct {
	Declared []string
	Adopted  []string
	Missing  []string
	Extra    []string
}

func (p Plan) Changes() int {
	return len(p.Adopted) + len(p.Missing) + len(p.Extra)
}

func (o Options) normalize() (Options, error) {
	if o.ManifestPath == "" {
		o.ManifestPath = "packages.yaml"
	}
	if o.StatePath == "" {
		stateHome := os.Getenv("XDG_STATE_HOME")
		if stateHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return o, fmt.Errorf("find home directory: %w", err)
			}
			stateHome = home + "/.local/state"
		}
		o.StatePath = stateHome + "/dotpkg/state.yaml"
	}
	if o.Profile == "" {
		o.Profile = "desktop"
	}
	if o.Profile != "desktop" && o.Profile != "server" {
		return o, fmt.Errorf("unknown profile: %s", o.Profile)
	}
	if o.Input == nil {
		o.Input = os.Stdin
	}
	if _, buffered := o.Input.(*bufio.Reader); !buffered {
		o.Input = bufio.NewReader(o.Input)
	}
	if o.Output == nil {
		o.Output = os.Stdout
	}
	if o.HostPath != "" {
		requestedHost := o.HostPath
		if _, err := os.Stat(o.HostPath); err == nil {
			if o.HostLabel == "" {
				o.HostLabel = requestedHost
			}
		} else {
			candidate := filepath.Join(filepath.Dir(o.ManifestPath), "hosts", requestedHost+".yaml")
			if _, candidateErr := os.Stat(candidate); candidateErr != nil {
				return o, fmt.Errorf("unknown host overlay: %s", requestedHost)
			}
			o.HostLabel = requestedHost
			o.HostPath = candidate
		}
	}
	return o, nil
}

func Load(options Options) (*manifest.Manifest, *state.State, Options, error) {
	options, err := options.normalize()
	if err != nil {
		return nil, nil, options, err
	}
	effective, err := manifest.Load(options.ManifestPath, options.HostPath)
	if err != nil {
		return nil, nil, options, err
	}
	currentState, err := state.Load(options.StatePath)
	if err != nil {
		return nil, nil, options, err
	}
	return effective, currentState, options, nil
}

func EnsureSelections(m *manifest.Manifest, s *state.State, options Options) (bool, error) {
	normalized, err := options.normalize()
	if err != nil {
		return false, err
	}
	options = normalized
	if options.Profile == "server" {
		return false, nil
	}
	changed := false
	selections := []struct {
		name  string
		label string
		path  string
	}{
		{name: "extras", label: "Desktop extras", path: "profiles.desktop.packages.extras"},
		{name: "hyprland", label: "Hyprland", path: "profiles.desktop.packages.hyprland"},
		{name: "i3", label: "i3/X11", path: "profiles.desktop.packages.i3"},
	}
	for _, selection := range selections {
		if _, recorded := s.Selection(selection.name); recorded {
			continue
		}
		if options.DryRun {
			return false, fmt.Errorf("no recorded desktop selection for %s; run sync without --dry-run first", selection.name)
		}
		packages := m.PackageNames(selection.path)
		fmt.Fprintf(options.Output, "\n%s includes:\n  %s\n", selection.label, strings.Join(packages, ", "))
		selected, err := askSelection(options, fmt.Sprintf("Install %s? [y/N] ", selection.label), false)
		if err != nil {
			return false, err
		}
		s.Set(selected, "current", "selections", selection.name)
		changed = true
	}
	for _, option := range m.OptionNames() {
		if _, recorded := s.Selection("options", option); recorded {
			continue
		}
		if options.DryRun {
			return false, fmt.Errorf("no recorded desktop selection for option %s; run sync without --dry-run first", option)
		}
		fmt.Fprintf(options.Output, "\nOptional set %s includes:\n  %s\n", option, strings.Join(m.OptionPackages(option), ", "))
		prompt := "Install optional set '" + option + "'? [y/N] "
		if configured, ok := m.OptionPrompt(option); ok {
			prompt = configured
			if !strings.HasSuffix(prompt, " ") {
				prompt += " "
			}
		}
		selected, err := askSelection(options, prompt, m.OptionDefault(option))
		if err != nil {
			return false, err
		}
		s.Set(selected, "current", "selections", "options", option)
		changed = true
	}
	return changed, nil
}

func askSelection(options Options, prompt string, defaultSelected bool) (bool, error) {
	if options.Yes {
		return true, nil
	}
	if _, err := fmt.Fprint(options.Output, prompt); err != nil {
		return false, err
	}
	reader, ok := options.Input.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(options.Input)
	}
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return defaultSelected, nil
	}
	return strings.HasPrefix(strings.ToLower(answer), "y"), nil
}

func PackageCategories(m *manifest.Manifest, profile string, selectedOnly bool, s *state.State) []string {
	categories := []string{
		"common.packages.headless",
		"profiles." + profile + ".packages." + profilePackageCategory(profile),
	}
	if profile != "desktop" {
		return categories
	}
	if !selectedOnly || selected(s, "extras") {
		categories = append(categories, "profiles.desktop.packages.extras")
	}
	if !selectedOnly || selected(s, "hyprland") {
		categories = append(categories, "profiles.desktop.packages.hyprland")
	}
	if !selectedOnly || selected(s, "i3") {
		categories = append(categories, "profiles.desktop.packages.i3")
	}
	for _, option := range m.OptionNames() {
		if !selectedOnly || selected(s, "options", option) {
			categories = append(categories, "profiles.desktop.options."+option+".packages")
		}
	}
	return categories
}

func profilePackageCategory(profile string) string {
	if profile == "desktop" {
		return "desktop"
	}
	return "headless"
}

func selected(s *state.State, path ...string) bool {
	value, recorded := s.Selection(path...)
	return recorded && value
}

func DeclaredPackages(m *manifest.Manifest, categories []string) []string {
	unique := make(map[string]struct{})
	for _, category := range categories {
		for _, packageName := range m.PackageNames(category) {
			unique[packageName] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for packageName := range unique {
		result = append(result, packageName)
	}
	sort.Strings(result)
	return result
}

func BuildPlan(ctx context.Context, m *manifest.Manifest, s *state.State, profile string, system backend.Backend) (Plan, error) {
	declared := DeclaredPackages(m, PackageCategories(m, profile, true, s))
	tracked := s.Packages()
	trackedSet := make(map[string]struct{}, len(tracked))
	for _, packageName := range tracked {
		trackedSet[packageName] = struct{}{}
	}
	plan := Plan{Declared: declared}
	for _, packageName := range declared {
		installed, err := system.IsInstalled(ctx, packageName)
		if err != nil {
			return plan, err
		}
		if installed {
			if _, tracked := trackedSet[packageName]; !tracked {
				plan.Adopted = append(plan.Adopted, packageName)
			}
		} else {
			plan.Missing = append(plan.Missing, packageName)
		}
	}
	declaredSet := make(map[string]struct{}, len(declared))
	for _, packageName := range declared {
		declaredSet[packageName] = struct{}{}
	}
	for _, packageName := range tracked {
		if _, declared := declaredSet[packageName]; !declared {
			plan.Extra = append(plan.Extra, packageName)
		}
	}
	sort.Strings(plan.Adopted)
	sort.Strings(plan.Missing)
	sort.Strings(plan.Extra)
	return plan, nil
}

func Sync(ctx context.Context, options Options, system backend.Backend) error {
	return WithLock(options, func(normalized Options) error {
		return syncLocked(ctx, normalized, system)
	})
}

// SyncLocked reconciles packages without acquiring a lock. Callers that need
// to update a manifest and then reconcile it as one transaction should invoke
// it from a WithLock callback.
func SyncLocked(ctx context.Context, options Options, system backend.Backend) error {
	normalized, err := options.normalize()
	if err != nil {
		return err
	}
	return syncLocked(ctx, normalized, system)
}

func syncLocked(ctx context.Context, options Options, system backend.Backend) error {
	m, s, options, err := Load(options)
	if err != nil {
		return err
	}
	selectionsChanged, err := EnsureSelections(m, s, options)
	if err != nil {
		return err
	}
	if selectionsChanged && !options.DryRun {
		setCurrentState(s, m, options)
		if err := s.Write(); err != nil {
			return err
		}
	}
	plan, err := BuildPlan(ctx, m, s, options.Profile, system)
	if err != nil {
		return err
	}
	printPlan(options.Output, plan)
	if plan.Changes() == 0 {
		fmt.Fprintln(options.Output, "packages: already synchronized")
		if !options.Resources {
			return nil
		}
	}
	if options.DryRun {
		return syncResources(ctx, m, s, options, system)
	}
	apply := options.Yes
	if !options.Yes {
		apply, err = askSelection(options, "Apply packages changes? [y/N] ", false)
		if err != nil {
			return err
		}
	}
	if !apply {
		return syncResources(ctx, m, s, options, system)
	}
	repository, aur, err := splitByOrigin(m, plan.Missing)
	if err != nil {
		return err
	}
	if err := system.Install(ctx, repository, aur, options.Profile); err != nil {
		return err
	}
	if err := system.Remove(ctx, plan.Extra, options.Profile); err != nil {
		return err
	}
	tracked := append([]string{}, s.Packages()...)
	tracked = subtract(tracked, plan.Extra)
	tracked = append(tracked, plan.Adopted...)
	tracked = append(tracked, plan.Missing...)
	s.SetPackages(tracked)
	setCurrentState(s, m, options)
	if err := s.Write(); err != nil {
		return err
	}
	return syncResources(ctx, m, s, options, system)
}

func syncResources(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system backend.Backend) error {
	if !options.Resources {
		return nil
	}
	resourceSystem := options.ResourceSystem
	if resourceSystem == nil {
		var ok bool
		resourceSystem, ok = system.(resource.System)
		if !ok {
			return fmt.Errorf("backend does not support resource reconciliation")
		}
	}
	if !options.DryRun {
		setCurrentState(s, m, options)
		if err := s.Write(); err != nil {
			return err
		}
	}
	return resource.Sync(ctx, m, s, resource.Options{
		Profile:  options.Profile,
		RootPath: options.RootPath,
		DryRun:   options.DryRun,
		Yes:      options.Yes,
		Replace:  options.Replace,
		Input:    options.Input,
		Output:   options.Output,
	}, resourceSystem)
}

func setCurrentState(s *state.State, m *manifest.Manifest, options Options) {
	s.Set(options.Profile, "current", "profile")
	host := options.HostLabel
	if host == "" {
		host = options.HostPath
	}
	s.Set(host, "current", "host")
	s.Set(m.Digest(), "current", "manifest_sha256")
}

func printPlan(output io.Writer, plan Plan) {
	fmt.Fprintln(output, "PACKAGES")
	for _, packageName := range plan.Adopted {
		fmt.Fprintf(output, "  ~ adopt: %s\n", packageName)
	}
	for _, packageName := range plan.Missing {
		fmt.Fprintf(output, "  + install: %s\n", packageName)
	}
	for _, packageName := range plan.Extra {
		fmt.Fprintf(output, "  - remove: %s\n", packageName)
	}
}

func splitByOrigin(m *manifest.Manifest, packages []string) ([]string, []string, error) {
	var repository, aur []string
	for _, packageName := range packages {
		switch m.PackageOrigin(packageName) {
		case "repo":
			repository = append(repository, packageName)
		case "aur":
			aur = append(aur, packageName)
		default:
			return nil, nil, fmt.Errorf("missing package origin: %s", packageName)
		}
	}
	return repository, aur, nil
}

func subtract(values, remove []string) []string {
	removeSet := make(map[string]struct{}, len(remove))
	for _, value := range remove {
		removeSet[value] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, found := removeSet[value]; !found {
			result = append(result, value)
		}
	}
	return result
}

type Validation struct {
	Packages []string
	Repo     []string
	AUR      []string
	Missing  []string
}

func Validate(ctx context.Context, m *manifest.Manifest, profile string, extra []string, system backend.Backend) (Validation, error) {
	packages := DeclaredPackages(m, PackageCategories(m, profile, false, nil))
	packages = append(packages, extra...)
	packages = uniqueSorted(packages)
	validation := Validation{Packages: packages}
	for _, packageName := range packages {
		switch m.PackageOrigin(packageName) {
		case "repo":
			validation.Repo = append(validation.Repo, packageName)
		case "aur":
			validation.AUR = append(validation.AUR, packageName)
		default:
			return validation, fmt.Errorf("missing package origin: %s", packageName)
		}
	}
	repositories, err := system.RepositoryPackages(ctx)
	if err != nil {
		return validation, err
	}
	for _, packageName := range validation.Repo {
		if _, found := repositories[packageName]; !found {
			validation.Missing = append(validation.Missing, packageName)
		}
	}
	aurPackages, err := system.AURPackages(ctx, validation.AUR)
	if err != nil {
		return validation, err
	}
	for _, packageName := range validation.AUR {
		if _, inAUR := aurPackages[packageName]; !inAUR {
			if _, inRepo := repositories[packageName]; !inRepo {
				validation.Missing = append(validation.Missing, packageName)
			}
		}
	}
	sort.Strings(validation.Missing)
	return validation, nil
}

func uniqueSorted(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		unique[value] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
