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

	"github.com/lukelex/dotpkg/internal/appimage"
	"github.com/lukelex/dotpkg/internal/backend"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/resource"
	"github.com/lukelex/dotpkg/internal/state"
)

type Options struct {
	ManifestPath    string
	HostPath        string
	HostLabel       string
	StatePath       string
	Profile         string
	DryRun          bool
	Yes             bool
	Resources       bool
	RootPath        string
	Replace         bool
	RestartServices bool
	ResourceSystem  resource.System
	AppImageSystem  appimage.System
	Input           io.Reader
	Output          io.Writer
	OutputFormat    string
}

type Plan struct {
	Declared []string
	Adopted  []string
	Missing  []string
	Extra    []string
}

type AppImagePlan struct {
	Plan
	Specs     map[string]appimage.Spec
	Artifacts map[string]appimage.Artifact
	Records   map[string]appimage.Record
	Previous  map[string]appimage.Record
}

// ConfigDir returns dotpkg's per-user configuration directory.
func ConfigDir() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".", ".config", "dotpkg")
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "dotpkg")
}

func ConfigManifestPath() string { return filepath.Join(ConfigDir(), "package.yaml") }

func ConfigStatePath() string { return filepath.Join(ConfigDir(), "state.yaml") }

// DefaultManifestPath returns the manifest selected when --manifest is not
// supplied. The environment override is useful for wrappers and installations
// that keep the manifest outside the current working directory. An initialized
// per-user manifest takes precedence over the legacy working-directory path.
func DefaultManifestPath() string {
	if path := os.Getenv("DOTPKG_MANIFEST"); path != "" {
		return path
	}
	if path := ConfigManifestPath(); fileExists(path) {
		return path
	}
	return "packages.yaml"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func defaultStatePath(manifestPath string) string {
	if samePath(manifestPath, ConfigManifestPath()) && fileExists(ConfigManifestPath()) {
		return ConfigStatePath()
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			stateHome = filepath.Join(home, ".local", "state")
		} else {
			stateHome = filepath.Join(".", ".local", "state")
		}
	}
	return filepath.Join(stateHome, "dotpkg", "state.yaml")
}

func samePath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && left == right
}

const initializedManifest = `source: repo
common:
  packages:
    headless: {}
profiles:
  desktop:
    packages:
      desktop: {}
      extras: {}
      hyprland: {}
      i3: {}
    options: {}
  server:
    packages:
      headless: {}
`

// Init creates the per-user manifest and state files without overwriting
// existing user data. It is intentionally idempotent for already-created
// files, which also allows a partially completed initialization to recover.
func Init(output io.Writer) error {
	directory := ConfigDir()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create dotpkg config directory: %w", err)
	}
	manifestPath := ConfigManifestPath()
	if !fileExists(manifestPath) {
		if err := writeAtomic(manifestPath, []byte(initializedManifest), 0o600); err != nil {
			return fmt.Errorf("create dotpkg manifest: %w", err)
		}
	}
	statePath := ConfigStatePath()
	if !fileExists(statePath) {
		initializedState := state.New(statePath)
		if err := initializedState.Write(); err != nil {
			return fmt.Errorf("create dotpkg state: %w", err)
		}
		if err := os.Chmod(statePath, 0o600); err != nil {
			return fmt.Errorf("protect dotpkg state: %w", err)
		}
	}
	if output != nil {
		_, _ = fmt.Fprintf(output, "dotpkg initialized in %s\nmanifest: %s\nstate: %s\n", directory, manifestPath, statePath)
	}
	return nil
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dotpkg-init-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
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
	return os.Rename(temporaryName, path)
}

func (p Plan) Changes() int {
	return len(p.Adopted) + len(p.Missing) + len(p.Extra)
}

func (o Options) normalize() (Options, error) {
	if o.ManifestPath == "" {
		o.ManifestPath = DefaultManifestPath()
	}
	if o.StatePath == "" {
		o.StatePath = defaultStatePath(o.ManifestPath)
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
	if o.OutputFormat == "" {
		o.OutputFormat = "text"
	}
	if o.OutputFormat != "text" && o.OutputFormat != "json" {
		return o, fmt.Errorf("unknown output format: %s", o.OutputFormat)
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
	declared := packageManagerPackages(m, profile, s)
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

func packageManagerPackages(m *manifest.Manifest, profile string, s *state.State) []string {
	declared := DeclaredPackages(m, PackageCategories(m, profile, true, s))
	result := make([]string, 0, len(declared))
	for _, packageName := range declared {
		if _, appImage := m.AppImage(packageName); !appImage {
			result = append(result, packageName)
		}
	}
	return result
}

func BuildAppImagePlan(ctx context.Context, m *manifest.Manifest, s *state.State, profile string, system appimage.System) (AppImagePlan, error) {
	paths := PackageCategories(m, profile, true, s)
	declaredSpecs := m.AppImages(paths)
	plan := AppImagePlan{
		Plan:      Plan{Declared: make([]string, 0, len(declaredSpecs))},
		Specs:     make(map[string]appimage.Spec, len(declaredSpecs)),
		Artifacts: make(map[string]appimage.Artifact, len(declaredSpecs)),
		Records:   make(map[string]appimage.Record, len(declaredSpecs)),
		Previous:  make(map[string]appimage.Record),
	}
	tracked := s.AppImages()
	trackedSet := make(map[string]struct{}, len(tracked))
	for name, record := range tracked {
		trackedSet[name] = struct{}{}
		plan.Previous[name] = stateAppImageRecord(name, record)
	}
	for _, spec := range declaredSpecs {
		plan.Declared = append(plan.Declared, spec.Name)
		plan.Specs[spec.Name] = appimage.Spec(spec)
		artifact, err := system.Resolve(ctx, appimage.Spec(spec))
		if err != nil {
			return plan, err
		}
		plan.Artifacts[spec.Name] = artifact
		target, err := system.Target(appimage.Spec(spec))
		if err != nil {
			return plan, err
		}
		record := appimage.Record{
			Name:      spec.Name,
			Address:   artifact.Address,
			Target:    target,
			Algorithm: artifact.Algorithm,
			Digest:    artifact.Digest,
			Version:   artifact.Version,
		}
		plan.Records[spec.Name] = record
		installed, err := system.Installed(ctx, record)
		if err != nil {
			return plan, err
		}
		if installed {
			previous, tracked := tracked[spec.Name]
			if !tracked {
				plan.Adopted = append(plan.Adopted, spec.Name)
			} else if previous.Address != artifact.Address || previous.Digest != artifact.Digest || previous.Algorithm != artifact.Algorithm || previous.Target != record.Target {
				plan.Adopted = append(plan.Adopted, spec.Name)
			}
			continue
		}
		plan.Missing = append(plan.Missing, spec.Name)
	}
	declaredSet := make(map[string]struct{}, len(plan.Declared))
	for _, name := range plan.Declared {
		declaredSet[name] = struct{}{}
	}
	for name := range tracked {
		if _, declared := declaredSet[name]; !declared {
			plan.Extra = append(plan.Extra, name)
		}
	}
	sort.Strings(plan.Declared)
	sort.Strings(plan.Adopted)
	sort.Strings(plan.Missing)
	sort.Strings(plan.Extra)
	return plan, nil
}

func BuildAppImageCleanPlan(m *manifest.Manifest, s *state.State, profile string) AppImagePlan {
	declared := m.AppImages(PackageCategories(m, profile, true, s))
	plan := AppImagePlan{
		Plan:      Plan{Declared: make([]string, 0, len(declared))},
		Previous:  make(map[string]appimage.Record),
		Specs:     make(map[string]appimage.Spec),
		Artifacts: make(map[string]appimage.Artifact),
	}
	for _, spec := range declared {
		plan.Declared = append(plan.Declared, spec.Name)
		plan.Specs[spec.Name] = appimage.Spec(spec)
	}
	for name, record := range s.AppImages() {
		plan.Previous[name] = stateAppImageRecord(name, record)
	}
	declaredSet := make(map[string]struct{}, len(plan.Declared))
	for _, name := range plan.Declared {
		declaredSet[name] = struct{}{}
	}
	for name := range plan.Previous {
		if _, ok := declaredSet[name]; !ok {
			plan.Extra = append(plan.Extra, name)
		}
	}
	sort.Strings(plan.Declared)
	sort.Strings(plan.Extra)
	return plan
}

func Clean(ctx context.Context, options Options, system backend.Backend) error {
	return WithLock(options, func(normalized Options) error {
		return cleanLocked(ctx, normalized, system)
	})
}

// SyncLocked reconciles packages without acquiring a lock. Callers that need
// to update a manifest and then reconcile it as one transaction should invoke
// it from a WithLock callback.

func setCurrentState(s *state.State, m *manifest.Manifest, options Options) {
	s.Set(options.Profile, "current", "profile")
	host := options.HostLabel
	if host == "" {
		host = options.HostPath
	}
	s.Set(host, "current", "host")
	s.Set(m.Digest(), "current", "manifest_sha256")
}

func splitByOrigin(m *manifest.Manifest, packages []string) ([]string, []string, error) {
	var repository, aur []string
	for _, packageName := range packages {
		switch m.PackageOrigin(packageName) {
		case "appimage":
			continue
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
		case "appimage":
			continue
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
