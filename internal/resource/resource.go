package resource

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

type System interface {
	CurrentGroups(context.Context, string) ([]string, error)
	GroupExists(context.Context, string) (bool, error)
	AddToGroup(context.Context, string, string) error
	RemoveFromGroup(context.Context, string, string) error
	ServiceExists(context.Context, bool, string) (bool, error)
	ServiceEnabled(context.Context, bool, string) (bool, error)
	ReloadServices(context.Context, bool) error
	EnableService(context.Context, bool, string) error
	DisableService(context.Context, bool, string) error
}

type Options struct {
	Profile  string
	RootPath string
	User     string
	DryRun   bool
	Yes      bool
	Replace  bool
	Input    io.Reader
	Output   io.Writer
}

type StagePlan struct {
	Declared []string
	Adopted  []string
	Missing  []string
	Extra    []string
}

func (p StagePlan) Changes() int {
	return len(p.Adopted) + len(p.Missing) + len(p.Extra)
}

type Plan struct {
	Groups   StagePlan
	Configs  StagePlan
	Services StagePlan
}

var errConfigConflict = errors.New("config target conflict")

func (p Plan) Changes() int {
	return p.Groups.Changes() + p.Configs.Changes() + p.Services.Changes()
}

func BuildPlan(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System) (Plan, error) {
	options = normalize(options, m)
	groups, err := planGroups(ctx, m, s, options, system)
	if err != nil {
		return Plan{}, err
	}
	configs, err := planConfigs(m, s, options)
	if err != nil {
		return Plan{}, err
	}
	services, err := planServices(ctx, m, s, options, system)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Groups: groups, Configs: configs, Services: services}, nil
}

func Sync(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System) error {
	options = normalize(options, m)
	plan, err := BuildPlan(ctx, m, s, options, system)
	if err != nil {
		return err
	}
	printPlan(options.Output, plan)
	if plan.Changes() == 0 || options.DryRun {
		return nil
	}

	reader, ok := options.Input.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(options.Input)
	}
	options.Input = reader
	for _, stage := range []struct {
		name  string
		plan  StagePlan
		apply func(StagePlan) error
		path  []string
	}{
		{name: "groups", plan: plan.Groups, apply: func(p StagePlan) error { return applyGroups(ctx, p, options, system) }, path: []string{"managed", "groups"}},
		{name: "configs", plan: plan.Configs, apply: func(p StagePlan) error { return applyConfigs(p, options) }, path: []string{"managed", "configs"}},
		{name: "services", plan: plan.Services, apply: func(p StagePlan) error { return applyServices(ctx, p, options, system) }, path: []string{"managed", "services"}},
	} {
		if stage.plan.Changes() == 0 {
			if stage.name == "configs" && !options.DryRun {
				if err := reloadForDeclaredConfigs(ctx, plan.Configs.Declared, options, system); err != nil {
					return err
				}
			}
			continue
		}
		if !options.Yes {
			apply, err := ask(options, "Apply "+stage.name+" changes? [y/N] ")
			if err != nil {
				return err
			}
			if !apply {
				continue
			}
		}
		if err := stage.apply(stage.plan); err != nil {
			return err
		}
		items := append([]string{}, s.Items(stage.path...)...)
		items = subtract(items, stage.plan.Extra)
		items = append(items, stage.plan.Adopted...)
		items = append(items, stage.plan.Missing...)
		if stage.name == "configs" {
			items = matchingConfigs(stage.plan.Declared, options)
			if err := reloadForDeclaredConfigs(ctx, plan.Configs.Declared, options, system); err != nil {
				return err
			}
		}
		s.SetItems(items, stage.path...)
	}
	return s.Write()
}

func reloadForDeclaredConfigs(ctx context.Context, declared []string, options Options, system System) error {
	var reloadSystem, reloadUser bool
	for _, mapping := range declared {
		_, target, err := configPaths(mapping, options)
		if err != nil {
			return err
		}
		path := filepath.ToSlash(target)
		switch {
		case strings.Contains(path, "/systemd/user/"):
			reloadUser = true
		case strings.Contains(path, "/systemd/system/"):
			reloadSystem = true
		}
	}
	if reloadSystem {
		if err := system.ReloadServices(ctx, false); err != nil {
			return err
		}
	}
	if reloadUser {
		if err := system.ReloadServices(ctx, true); err != nil {
			return err
		}
	}
	return nil
}

func normalize(options Options, m *manifest.Manifest) Options {
	if options.Profile == "" {
		options.Profile = "desktop"
	}
	if options.RootPath == "" {
		options.RootPath = filepath.Dir(filepath.Dir(m.Path))
	}
	if options.User == "" {
		if current, err := user.Current(); err == nil {
			options.User = current.Username
		}
	}
	if options.Input == nil {
		options.Input = os.Stdin
	}
	if options.Output == nil {
		options.Output = os.Stdout
	}
	return options
}

func categoryPaths(m *manifest.Manifest, profile string, s *state.State) []string {
	paths := []string{
		"common.packages",
		"profiles." + profile + ".packages." + profileCategory(profile),
	}
	if profile != "desktop" {
		return paths
	}
	paths = append(paths, "profiles.desktop.packages.dotfiles")
	for _, selection := range []string{"extras", "hyprland", "i3"} {
		if selected, recorded := s.Selection(selection); recorded && selected {
			paths = append(paths, "profiles.desktop.packages."+selection)
		}
	}
	for _, option := range m.OptionNames() {
		if selected, recorded := s.Selection("options", option); recorded && selected {
			paths = append(paths, "profiles.desktop.options."+option+".packages")
		}
	}
	return paths
}

func profileCategory(profile string) string {
	if profile == "desktop" {
		return "desktop"
	}
	return "headless"
}

func declaredMetadata(m *manifest.Manifest, s *state.State, profile, key string) []string {
	values := []string{}
	for _, path := range categoryPaths(m, profile, s) {
		values = append(values, m.MetadataStrings(path, key)...)
	}
	if key == "configs" {
		values = append(values, m.ResourceConfigs(profile, selectionValues(m, s))...)
	}
	return unique(values)
}

func declaredServices(m *manifest.Manifest, s *state.State, profile string) []string {
	var values []string
	for _, path := range categoryPaths(m, profile, s) {
		for _, service := range m.ServiceNames(path, "system") {
			values = append(values, service)
		}
		for _, service := range m.ServiceNames(path, "user") {
			values = append(values, "user:"+service)
		}
	}
	values = append(values, m.ResourceServices(profile, selectionValues(m, s))...)
	return unique(values)
}

func selectionValues(m *manifest.Manifest, s *state.State) map[string]bool {
	values := make(map[string]bool)
	for _, selection := range []string{"extras", "hyprland", "i3"} {
		selected, recorded := s.Selection(selection)
		values[selection] = recorded && selected
	}
	for _, option := range m.OptionNames() {
		selected, recorded := s.Selection("options", option)
		values["options."+option] = recorded && selected
	}
	return values
}

func planGroups(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System) (StagePlan, error) {
	declared := declaredMetadata(m, s, options.Profile, "groups")
	current, err := system.CurrentGroups(ctx, options.User)
	if err != nil {
		return StagePlan{}, err
	}
	currentSet := stringSet(current)
	planned := StagePlan{Declared: declared}
	for _, group := range declared {
		exists, err := system.GroupExists(ctx, group)
		if err != nil {
			return StagePlan{}, err
		}
		if !exists {
			continue
		}
		if _, present := currentSet[group]; present {
			if !contains(s.Items("managed", "groups"), group) {
				planned.Adopted = append(planned.Adopted, group)
			}
		} else {
			planned.Missing = append(planned.Missing, group)
		}
	}
	planned.Extra = subtract(s.Items("managed", "groups"), declared)
	return sortPlan(planned), nil
}

func planConfigs(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
	declared := declaredMetadata(m, s, options.Profile, "configs")
	planned := StagePlan{Declared: declared}
	tracked := s.Items("managed", "configs")
	trackedSet := stringSet(tracked)
	for _, mapping := range declared {
		source, target, err := configPaths(mapping, options)
		if err != nil {
			return StagePlan{}, err
		}
		matches, err := configMatches(target, source)
		if err != nil {
			return StagePlan{}, err
		}
		if matches {
			if !s.Exists {
				if _, tracked := trackedSet[mapping]; !tracked {
					planned.Adopted = append(planned.Adopted, mapping)
				}
			}
		} else {
			planned.Missing = append(planned.Missing, mapping)
		}
	}
	planned.Extra = subtract(tracked, declared)
	return sortPlan(planned), nil
}

func planServices(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System) (StagePlan, error) {
	declared := declaredServices(m, s, options.Profile)
	configs := declaredMetadata(m, s, options.Profile, "configs")
	planned := StagePlan{Declared: declared}
	trackedSet := stringSet(s.Items("managed", "services"))
	for _, service := range declared {
		userService, name := strings.HasPrefix(service, "user:"), strings.TrimPrefix(service, "user:")
		exists, err := system.ServiceExists(ctx, userService, name)
		if err != nil {
			return StagePlan{}, err
		}
		if !exists && configProvidesService(configs, service, options) {
			exists = true
		}
		if !exists {
			continue
		}
		enabled, err := system.ServiceEnabled(ctx, userService, name)
		if err != nil {
			return StagePlan{}, err
		}
		if enabled {
			if _, tracked := trackedSet[service]; !tracked {
				planned.Adopted = append(planned.Adopted, service)
			}
		} else {
			planned.Missing = append(planned.Missing, service)
		}
	}
	planned.Extra = subtract(s.Items("managed", "services"), declared)
	return sortPlan(planned), nil
}

func configProvidesService(configs []string, service string, options Options) bool {
	userService := strings.HasPrefix(service, "user:")
	name := strings.TrimPrefix(service, "user:")
	suffix := "/systemd/system/" + name
	if userService {
		suffix = "/systemd/user/" + name
	}
	for _, mapping := range configs {
		source, target, err := configPaths(mapping, options)
		if err != nil || !strings.HasSuffix(filepath.ToSlash(target), suffix) {
			continue
		}
		if _, err := os.Stat(source); err == nil {
			return true
		}
	}
	return false
}

func applyGroups(ctx context.Context, plan StagePlan, options Options, system System) error {
	for _, group := range plan.Missing {
		if err := system.AddToGroup(ctx, options.User, group); err != nil {
			return err
		}
	}
	for _, group := range plan.Extra {
		if err := system.RemoveFromGroup(ctx, options.User, group); err != nil {
			return err
		}
	}
	return nil
}

func applyConfigs(plan StagePlan, options Options) error {
	for _, mapping := range plan.Missing {
		source, target, err := configPaths(mapping, options)
		if err != nil {
			return err
		}
		if err := linkConfig(source, target, options.Replace); err != nil {
			if errors.Is(err, errConfigConflict) {
				fmt.Fprintf(options.Output, "skipping %s: exists and is not a repo link; use --replace to overwrite\n", target)
				continue
			}
			return err
		}
	}
	for _, mapping := range plan.Extra {
		source, target, err := configPaths(mapping, options)
		if err != nil {
			return err
		}
		matches, err := configMatches(target, source)
		if err != nil {
			return err
		}
		if matches {
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("remove config link %s: %w", target, err)
			}
		}
	}
	return nil
}

func applyServices(ctx context.Context, plan StagePlan, options Options, system System) error {
	for _, service := range plan.Missing {
		userService, name := strings.HasPrefix(service, "user:"), strings.TrimPrefix(service, "user:")
		if err := system.EnableService(ctx, userService, name); err != nil {
			return err
		}
	}
	for _, service := range plan.Extra {
		userService, name := strings.HasPrefix(service, "user:"), strings.TrimPrefix(service, "user:")
		if err := system.DisableService(ctx, userService, name); err != nil {
			return err
		}
	}
	return nil
}

func configPaths(mapping string, options Options) (string, string, error) {
	parts := strings.SplitN(mapping, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid config mapping: %s", mapping)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("find home directory: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	target := strings.ReplaceAll(parts[1], "$HOME", home)
	target = strings.ReplaceAll(target, "$XDG_CONFIG_HOME", configHome)
	return filepath.Join(options.RootPath, parts[0]), target, nil
}

func configMatches(target, source string) (bool, error) {
	actual, err := os.Readlink(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, nil
	}
	return actual == source, nil
}

func linkConfig(source, target string, replace bool) error {
	if _, err := os.Lstat(target); err == nil {
		matches, matchErr := configMatches(target, source)
		if matchErr != nil {
			return matchErr
		}
		if matches {
			return nil
		}
		if !replace {
			return fmt.Errorf("%w: %s", errConfigConflict, target)
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("replace config target %s: %w", target, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect config target %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.Symlink(source, target); err != nil {
		return fmt.Errorf("link config %s: %w", target, err)
	}
	return nil
}

func matchingConfigs(declared []string, options Options) []string {
	matching := make([]string, 0, len(declared))
	for _, mapping := range declared {
		source, target, err := configPaths(mapping, options)
		if err != nil {
			continue
		}
		matches, err := configMatches(target, source)
		if err == nil && matches {
			matching = append(matching, mapping)
		}
	}
	return matching
}

func printPlan(output io.Writer, plan Plan) {
	printStage(output, "GROUPS", plan.Groups)
	printStage(output, "CONFIGS", plan.Configs)
	printStage(output, "SERVICES", plan.Services)
}

func printStage(output io.Writer, name string, plan StagePlan) {
	if plan.Changes() == 0 {
		return
	}
	fmt.Fprintln(output, name)
	for _, value := range plan.Adopted {
		fmt.Fprintf(output, "  ~ adopt: %s\n", value)
	}
	for _, value := range plan.Missing {
		fmt.Fprintf(output, "  + add: %s\n", value)
	}
	for _, value := range plan.Extra {
		fmt.Fprintf(output, "  - remove: %s\n", value)
	}
}

func ask(options Options, prompt string) (bool, error) {
	if _, err := fmt.Fprint(options.Output, prompt); err != nil {
		return false, err
	}
	answer, err := options.Input.(*bufio.Reader).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y"), nil
}

func sortPlan(plan StagePlan) StagePlan {
	sort.Strings(plan.Declared)
	sort.Strings(plan.Adopted)
	sort.Strings(plan.Missing)
	sort.Strings(plan.Extra)
	return plan
}

func unique(values []string) []string {
	set := stringSet(values)
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func contains(values []string, wanted string) bool {
	_, ok := stringSet(values)[wanted]
	return ok
}

func subtract(values, remove []string) []string {
	removeSet := stringSet(remove)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, found := removeSet[value]; !found {
			result = append(result, value)
		}
	}
	return unique(result)
}
