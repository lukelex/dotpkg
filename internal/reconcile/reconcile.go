package reconcile

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/lukelex/dotpkg/internal/recovery"
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

type PlanChange struct {
	Action string `json:"action"`
	Item   string `json:"item"`
}

type PlanStage struct {
	Name     string       `json:"name"`
	Declared []string     `json:"declared"`
	Adopted  []string     `json:"adopted"`
	Missing  []string     `json:"missing"`
	Extra    []string     `json:"extra"`
	Changes  []PlanChange `json:"changes"`
}

// PlanDocument is the stable machine-readable plan envelope. The schema name
// and version are intentionally explicit so callers can reject incompatible
// output instead of guessing from fields.
type PlanDocument struct {
	Schema   string      `json:"schema"`
	Version  int         `json:"version"`
	Manifest string      `json:"manifest"`
	Profile  string      `json:"profile"`
	Host     string      `json:"host,omitempty"`
	Changes  int         `json:"changes"`
	Stages   []PlanStage `json:"stages"`
}

func NewPlanDocument(packagePlan Plan, resourcePlan *resource.Plan, options Options, appImagePlans ...AppImagePlan) PlanDocument {
	document := PlanDocument{
		Schema:   "dotpkg.plan",
		Version:  1,
		Manifest: options.ManifestPath,
		Profile:  options.Profile,
		Host:     options.HostLabel,
		Stages:   []PlanStage{planStage("packages", packagePlan.Declared, packagePlan.Adopted, packagePlan.Missing, packagePlan.Extra, "install")},
	}
	if len(appImagePlans) > 0 {
		plan := appImagePlans[0]
		document.Stages = append(document.Stages, planStage("appimages", plan.Declared, plan.Adopted, plan.Missing, plan.Extra, "install"))
	}
	if document.Host == "" {
		document.Host = options.HostPath
	}
	if resourcePlan != nil {
		document.Stages = append(document.Stages,
			resourceStage("groups", resourcePlan.Groups),
			resourceStage("configs", resourcePlan.Configs),
			resourceStage("services", resourcePlan.Services),
		)
		if resourcePlan.ExecutableLinks.Changes() > 0 || len(resourcePlan.ExecutableLinks.Declared) > 0 {
			document.Stages = append(document.Stages, resourceStage("executable_links", resourcePlan.ExecutableLinks))
		}
	}
	for _, stage := range document.Stages {
		document.Changes += len(stage.Changes)
	}
	return document
}

func planStage(name string, declared, adopted, missing, extra []string, missingAction string) PlanStage {
	stage := PlanStage{
		Name:     name,
		Declared: copyStrings(declared),
		Adopted:  copyStrings(adopted),
		Missing:  copyStrings(missing),
		Extra:    copyStrings(extra),
		Changes:  make([]PlanChange, 0, len(adopted)+len(missing)+len(extra)),
	}
	for _, item := range adopted {
		stage.Changes = append(stage.Changes, PlanChange{Action: "adopt", Item: item})
	}
	for _, item := range missing {
		stage.Changes = append(stage.Changes, PlanChange{Action: missingAction, Item: item})
	}
	for _, item := range extra {
		stage.Changes = append(stage.Changes, PlanChange{Action: "remove", Item: item})
	}
	return stage
}

func resourceStage(name string, plan resource.StagePlan) PlanStage {
	return planStage(name, plan.Declared, plan.Adopted, plan.Missing, plan.Extra, "add")
}

func copyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string{}, values...)
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

func Sync(ctx context.Context, options Options, system backend.Backend) error {
	return WithLock(options, func(normalized Options) error {
		return syncLocked(ctx, normalized, system)
	})
}

func Clean(ctx context.Context, options Options, system backend.Backend) error {
	return WithLock(options, func(normalized Options) error {
		return cleanLocked(ctx, normalized, system)
	})
}

func Recover(ctx context.Context, options Options, system backend.Backend) error {
	normalized, err := options.normalize()
	if err != nil {
		return err
	}
	journal, err := recovery.Load(normalized.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(normalized.Output, "recovery: no pending package transaction")
		return nil
	}
	if err != nil {
		return err
	}
	if journal.Completed {
		return recovery.Clear(normalized.StatePath)
	}
	if !normalized.Yes {
		apply, askErr := askSelection(normalized, "Rollback the recorded package transaction? [y/N] ", false)
		if askErr != nil {
			return askErr
		}
		if !apply {
			return nil
		}
	}
	appSystem, err := appImageBackend(normalized)
	if err != nil {
		return err
	}
	if err := rollback(ctx, system, appSystem, journal); err != nil {
		return fmt.Errorf("recovery failed; journal retained: %w", err)
	}
	return recovery.Clear(normalized.StatePath)
}

func cleanLocked(ctx context.Context, options Options, system backend.Backend) error {
	m, s, options, err := Load(options)
	if err != nil {
		return err
	}
	declared := packageManagerPackages(m, options.Profile, s)
	packagePlan := Plan{Declared: declared, Extra: subtract(s.Packages(), declared)}
	appImagePlan := BuildAppImageCleanPlan(m, s, options.Profile)
	var resourcePlan *resource.Plan
	if options.Resources {
		resourceOptions := resource.Options{Profile: options.Profile, RootPath: options.RootPath, Output: options.Output}
		planned := resource.CleanPlan(m, s, resourceOptions)
		resourcePlan = &planned
	}
	printPlan(options.Output, packagePlan, options.OutputFormat, resourcePlan, options, appImagePlan)
	changes := len(packagePlan.Extra) + len(appImagePlan.Extra)
	if resourcePlan != nil {
		changes += resourcePlan.Changes()
	}
	if changes == 0 {
		return nil
	}
	if options.DryRun {
		return nil
	}
	if !options.Yes {
		apply, askErr := askSelection(options, "Remove managed items no longer declared? [y/N] ", false)
		if askErr != nil {
			return askErr
		}
		if !apply {
			return nil
		}
	}
	oldPackages := append([]string{}, s.Packages()...)
	oldOrigins := s.PackageOrigins()
	oldState, err := s.Snapshot()
	if err != nil {
		return err
	}
	journal := recovery.New(options.StatePath, options.Profile, recovery.Packages{}, packageGroupFromState(packagePlan.Extra, oldOrigins, m))
	for _, name := range appImagePlan.Extra {
		journal.AppImages.Removed = append(journal.AppImages.Removed, recoveryAppImageRecord(appImagePlan.Previous[name]))
	}
	var appSystem appimage.System
	if len(appImagePlan.Extra) > 0 {
		appSystem, err = appImageBackend(options)
		if err != nil {
			return err
		}
	}
	if len(packagePlan.Extra) > 0 || len(appImagePlan.Extra) > 0 {
		if err := journal.Write(); err != nil {
			return err
		}
	}
	if len(packagePlan.Extra) > 0 {
		if err := system.Remove(ctx, packagePlan.Extra, options.Profile); err != nil {
			return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
	}
	if len(appImagePlan.Extra) > 0 {
		for _, name := range appImagePlan.Extra {
			if err := appSystem.Remove(ctx, appImagePlan.Previous[name]); err != nil {
				return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
			}
		}
	}
	if len(packagePlan.Extra) > 0 {
		s.SetPackages(subtract(s.Packages(), packagePlan.Extra))
		origins := s.PackageOrigins()
		for _, packageName := range packagePlan.Extra {
			delete(origins, packageName)
		}
		s.SetPackageOrigins(origins)
		setCurrentState(s, m, options)
	}
	if len(appImagePlan.Extra) > 0 {
		images := s.AppImages()
		for _, name := range appImagePlan.Extra {
			delete(images, name)
		}
		s.SetAppImages(images)
	}
	if len(packagePlan.Extra) > 0 || len(appImagePlan.Extra) > 0 {
		setCurrentState(s, m, options)
		if err := s.Write(); err != nil {
			return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
	}
	if resourcePlan != nil && resourcePlan.Changes() > 0 {
		resourceSystem, backendErr := resourceBackend(options, system)
		if backendErr != nil {
			return backendErr
		}
		setCurrentState(s, m, options)
		if err := resource.Clean(ctx, m, s, resource.Options{
			Profile:  options.Profile,
			RootPath: options.RootPath,
			Output:   options.Output,
		}, resourceSystem); err != nil {
			if len(packagePlan.Extra) > 0 || len(appImagePlan.Extra) > 0 {
				return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
			}
			return err
		}
	}
	if len(packagePlan.Extra) > 0 || len(appImagePlan.Extra) > 0 {
		journal.Completed = true
		if err := journal.Write(); err != nil {
			return fmt.Errorf("complete recovery journal: %w", err)
		}
		if err := recovery.Clear(options.StatePath); err != nil {
			return err
		}
	}
	return nil
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
	appSystem, err := appImageBackend(options)
	if err != nil {
		return err
	}
	appImagePlan, err := BuildAppImagePlan(ctx, m, s, options.Profile, appSystem)
	if err != nil {
		return err
	}
	var resourcePlan *resource.Plan
	if options.Resources && options.OutputFormat == "json" {
		resourceSystem, resourceErr := resourceBackend(options, system)
		if resourceErr != nil {
			return resourceErr
		}
		planned, resourceErr := resource.BuildPlan(ctx, m, s, resource.Options{
			Profile:  options.Profile,
			RootPath: options.RootPath,
			Output:   options.Output,
		}, resourceSystem)
		if resourceErr != nil {
			return resourceErr
		}
		resourcePlan = &planned
	}
	printPlan(options.Output, plan, options.OutputFormat, resourcePlan, options, appImagePlan)
	if plan.Changes() == 0 && appImagePlan.Changes() == 0 {
		if options.OutputFormat == "text" {
			fmt.Fprintln(options.Output, "packages and AppImages: already synchronized")
		}
		if !options.Resources {
			return nil
		}
	}
	if options.DryRun {
		return syncResources(ctx, m, s, options, system)
	}
	apply := options.Yes
	if !options.Yes {
		apply, err = askSelection(options, "Apply package and AppImage changes? [y/N] ", false)
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
	oldPackages := append([]string{}, s.Packages()...)
	oldOrigins := s.PackageOrigins()
	oldState, err := s.Snapshot()
	if err != nil {
		return err
	}
	journal := recovery.New(options.StatePath, options.Profile,
		packageGroup(repository, aur, nil),
		packageGroupFromState(plan.Extra, oldOrigins, m),
	)
	for _, name := range appImagePlan.Missing {
		journal.AppImages.Installed = append(journal.AppImages.Installed, recoveryAppImageRecord(appImagePlan.Records[name]))
		if previous, ok := appImagePlan.Previous[name]; ok {
			journal.AppImages.Removed = append(journal.AppImages.Removed, recoveryAppImageRecord(previous))
		}
	}
	for _, name := range appImagePlan.Extra {
		journal.AppImages.Removed = append(journal.AppImages.Removed, recoveryAppImageRecord(appImagePlan.Previous[name]))
	}
	packageChanges := len(plan.Missing) + len(plan.Extra)
	appImageChanges := appImagePlan.Changes()
	if packageChanges > 0 || appImageChanges > 0 {
		if err := journal.Write(); err != nil {
			return err
		}
	}
	if err := system.Install(ctx, repository, aur, options.Profile); err != nil {
		return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
	}
	if err := system.Remove(ctx, plan.Extra, options.Profile); err != nil {
		return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
	}
	for _, name := range appImagePlan.Missing {
		if _, err := appSystem.Install(ctx, appImagePlan.Specs[name], appImagePlan.Artifacts[name]); err != nil {
			return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
		if previous, ok := appImagePlan.Previous[name]; ok && previous.Target != appImagePlan.Records[name].Target {
			if err := appSystem.Remove(ctx, previous); err != nil {
				return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
			}
		}
	}
	for _, name := range appImagePlan.Extra {
		if err := appSystem.Remove(ctx, appImagePlan.Previous[name]); err != nil {
			return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
	}
	tracked := append([]string{}, s.Packages()...)
	tracked = subtract(tracked, plan.Extra)
	tracked = append(tracked, plan.Adopted...)
	tracked = append(tracked, plan.Missing...)
	s.SetPackages(tracked)
	origins := s.PackageOrigins()
	for _, packageName := range plan.Extra {
		delete(origins, packageName)
	}
	for _, packageName := range tracked {
		if origin := m.PackageOrigin(packageName); origin == "repo" || origin == "aur" {
			origins[packageName] = origin
		}
	}
	s.SetPackageOrigins(origins)
	images := s.AppImages()
	for _, name := range appImagePlan.Extra {
		delete(images, name)
	}
	for _, name := range append(append([]string{}, appImagePlan.Adopted...), appImagePlan.Missing...) {
		images[name] = state.AppImage{
			Address:   appImagePlan.Records[name].Address,
			Target:    appImagePlan.Records[name].Target,
			Algorithm: appImagePlan.Records[name].Algorithm,
			Digest:    appImagePlan.Records[name].Digest,
			Version:   appImagePlan.Records[name].Version,
		}
	}
	s.SetAppImages(images)
	setCurrentState(s, m, options)
	if err := s.Write(); err != nil {
		return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
	}
	if err := syncResources(ctx, m, s, options, system); err != nil {
		if packageChanges > 0 || appImageChanges > 0 {
			return packageFailure(err, ctx, system, appSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
		return err
	}
	if packageChanges > 0 || appImageChanges > 0 {
		journal.Completed = true
		if err := journal.Write(); err != nil {
			return fmt.Errorf("complete recovery journal: %w", err)
		}
		if err := recovery.Clear(options.StatePath); err != nil {
			return err
		}
	}
	return nil
}

func packageGroup(repository, aur, unknown []string) recovery.Packages {
	return recovery.Packages{Repository: append([]string{}, repository...), AUR: append([]string{}, aur...), Unknown: append([]string{}, unknown...)}
}

func stateAppImageRecord(name string, image state.AppImage) appimage.Record {
	return appimage.Record{
		Name:      name,
		Address:   image.Address,
		Target:    image.Target,
		Algorithm: image.Algorithm,
		Digest:    image.Digest,
		Version:   image.Version,
	}
}

func recoveryAppImageRecord(record appimage.Record) recovery.AppImage {
	return recovery.AppImage{
		Name:      record.Name,
		Address:   record.Address,
		Target:    record.Target,
		Algorithm: record.Algorithm,
		Digest:    record.Digest,
		Version:   record.Version,
	}
}

func appImageRecordFromRecovery(record recovery.AppImage) appimage.Record {
	return appimage.Record{
		Name:      record.Name,
		Address:   record.Address,
		Target:    record.Target,
		Algorithm: record.Algorithm,
		Digest:    record.Digest,
		Version:   record.Version,
	}
}

func packageGroupFromState(packages []string, origins map[string]string, m *manifest.Manifest) recovery.Packages {
	var repository, aur, unknown []string
	for _, packageName := range packages {
		origin := origins[packageName]
		if origin == "" {
			origin = m.PackageOrigin(packageName)
		}
		switch origin {
		case "repo":
			repository = append(repository, packageName)
		case "aur":
			aur = append(aur, packageName)
		default:
			unknown = append(unknown, packageName)
		}
	}
	return packageGroup(repository, aur, unknown)
}

func packageFailure(original error, ctx context.Context, system backend.Backend, appSystem appimage.System, journal recovery.Journal, s *state.State, oldState map[string]any, oldPackages []string, oldOrigins map[string]string) error {
	rollbackErr := rollback(ctx, system, appSystem, journal)
	s.Restore(oldState)
	s.SetPackages(oldPackages)
	s.SetPackageOrigins(oldOrigins)
	stateErr := s.Write()
	if rollbackErr != nil || stateErr != nil {
		return fmt.Errorf("%w; recovery required (rollback=%v, state=%v)", original, rollbackErr, stateErr)
	}
	if err := recovery.Clear(journal.StatePath); err != nil {
		return fmt.Errorf("%w; remove recovery journal: %v", original, err)
	}
	return original
}

func rollback(ctx context.Context, system backend.Backend, appSystem appimage.System, journal recovery.Journal) error {
	var errorsFound []string
	var installed []string
	for _, packageName := range append(append([]string{}, journal.Installed.Repository...), journal.Installed.AUR...) {
		present, err := system.IsInstalled(ctx, packageName)
		if err != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("check installed %s: %v", packageName, err))
		} else if present {
			installed = append(installed, packageName)
		}
	}
	installed = append(installed, journal.Installed.Unknown...)
	if len(installed) > 0 {
		if err := system.Remove(ctx, installed, journal.Profile); err != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("remove newly installed packages: %v", err))
		}
	}
	if len(journal.Removed.Unknown) > 0 {
		errorsFound = append(errorsFound, "cannot restore packages with unknown origins: "+strings.Join(journal.Removed.Unknown, ", "))
	}
	if len(journal.Removed.Repository) > 0 || len(journal.Removed.AUR) > 0 {
		if err := system.Install(ctx, journal.Removed.Repository, journal.Removed.AUR, journal.Profile); err != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("restore removed packages: %v", err))
		}
	}
	if appSystem != nil {
		for _, record := range journal.AppImages.Installed {
			appRecord := appImageRecordFromRecovery(record)
			present, err := appSystem.Installed(ctx, appRecord)
			if err != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("check newly installed AppImage %s: %v", record.Name, err))
			} else if present {
				err = appSystem.Remove(ctx, appRecord)
			}
			if err != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("remove newly installed AppImage %s: %v", record.Name, err))
			}
		}
		for _, record := range journal.AppImages.Removed {
			appRecord := appImageRecordFromRecovery(record)
			spec := appimage.Spec{Name: appRecord.Name, Address: appRecord.Address, Target: appRecord.Target}
			artifact := appimage.Artifact{Address: appRecord.Address, Algorithm: appRecord.Algorithm, Digest: appRecord.Digest, Version: appRecord.Version}
			if _, err := appSystem.Install(ctx, spec, artifact); err != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("restore removed AppImage %s: %v", record.Name, err))
			}
		}
	}
	if len(errorsFound) > 0 {
		return errors.New(strings.Join(errorsFound, "; "))
	}
	return nil
}

func appImageBackend(options Options) (appimage.System, error) {
	if options.AppImageSystem != nil {
		return options.AppImageSystem, nil
	}
	return appimage.New(), nil
}

func syncResources(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system backend.Backend) error {
	if !options.Resources {
		return nil
	}
	resourceSystem, err := resourceBackend(options, system)
	if err != nil {
		return err
	}
	if !options.DryRun {
		setCurrentState(s, m, options)
		if err := s.Write(); err != nil {
			return err
		}
	}
	return resource.Sync(ctx, m, s, resource.Options{
		Profile:         options.Profile,
		RootPath:        options.RootPath,
		DryRun:          options.DryRun,
		Yes:             options.Yes,
		Replace:         options.Replace,
		RestartServices: options.RestartServices,
		Input:           options.Input,
		Output:          options.Output,
		OutputFormat:    options.OutputFormat,
		SuppressPlan:    options.OutputFormat == "json",
	}, resourceSystem)
}

func resourceBackend(options Options, system backend.Backend) (resource.System, error) {
	if options.ResourceSystem != nil {
		return options.ResourceSystem, nil
	}
	resourceSystem, ok := system.(resource.System)
	if !ok {
		return nil, fmt.Errorf("backend does not support resource reconciliation")
	}
	return resourceSystem, nil
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

func printPlan(output io.Writer, plan Plan, format string, resourcePlan *resource.Plan, options Options, appImagePlans ...AppImagePlan) {
	if format == "json" {
		if len(appImagePlans) > 0 {
			_ = json.NewEncoder(output).Encode(NewPlanDocument(plan, resourcePlan, options, appImagePlans[0]))
		} else {
			_ = json.NewEncoder(output).Encode(NewPlanDocument(plan, resourcePlan, options))
		}
		return
	}
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
	if len(appImagePlans) > 0 {
		appPlan := appImagePlans[0]
		if appPlan.Changes() > 0 {
			fmt.Fprintln(output, "APPIMAGES")
			for _, name := range appPlan.Adopted {
				fmt.Fprintf(output, "  ~ adopt: %s\n", name)
			}
			for _, name := range appPlan.Missing {
				fmt.Fprintf(output, "  + install: %s\n", name)
			}
			for _, name := range appPlan.Extra {
				fmt.Fprintf(output, "  - remove: %s\n", name)
			}
		}
	}
	if resourcePlan != nil {
		for _, stage := range []struct {
			name string
			plan resource.StagePlan
		}{
			{name: "GROUPS", plan: resourcePlan.Groups},
			{name: "CONFIGS", plan: resourcePlan.Configs},
			{name: "SERVICES", plan: resourcePlan.Services},
		} {
			if len(stage.plan.Extra) == 0 {
				continue
			}
			fmt.Fprintln(output, stage.name)
			for _, item := range stage.plan.Extra {
				fmt.Fprintf(output, "  - remove: %s\n", item)
			}
		}
	}
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
