package resource

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

// ServiceAction describes a service change triggered by reconciliation.
type ServiceAction struct {
	Service      string   `json:"service"`
	Action       string   `json:"action"`
	Reasons      []string `json:"reasons"`
	DaemonReload bool     `json:"daemon_reload"`
}

type serviceInput struct {
	path, action string
	unit         bool
}

// ServiceTracker tracks service changes while resources are reconciled.
type ServiceTracker struct {
	records  map[string]state.ServiceTracking
	inputs   map[string]map[string]serviceInput
	packages map[string][]string
}

// ServiceActionError marks a post-reconciliation failure: package changes have
// already succeeded and must not be rolled back for a failed service action.
type ServiceActionError struct{ Err error }

func (e *ServiceActionError) Error() string { return e.Err.Error() }
func (e *ServiceActionError) Unwrap() error { return e.Err }

// PrepareServices captures inputs before any package or resource mutations.
// It does not write state or change services, so it is also safe for previews.
func PrepareServices(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System) (*ServiceTracker, error) {
	options = normalize(options, m)
	records, err := s.ServiceTracking()
	if err != nil {
		return nil, err
	}
	t := &ServiceTracker{records: records, inputs: map[string]map[string]serviceInput{}, packages: map[string][]string{}}
	deps := m.ServiceDependencies(categoryPaths(m, options.Profile, s), options.Profile, selectionValues(m, s))
	for _, service := range declaredServices(m, s, options.Profile) {
		t.inputs[service] = map[string]serviceInput{}
		add := func(path, action string, unit bool) {
			path = filepath.Clean(path)
			old, exists := t.inputs[service][path]
			if exists {
				unit = unit || old.unit
				if old.action == "restart" {
					action = "restart"
				}
			}
			if unit {
				action = "restart"
			}
			t.inputs[service][path] = serviceInput{path, action, unit}
		}
		for _, dep := range deps[service] {
			if dep.Package != "" {
				t.packages[service] = append(t.packages[service], dep.Package)
				continue
			}
			path, e := dependencyPath(dep.Path)
			if e != nil {
				return nil, e
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(options.RootPath, path)
			}
			add(path, dep.Action, false)
		}
		user := strings.HasPrefix(service, "user:")
		name := strings.TrimPrefix(service, "user:")
		if files, ok := system.(interface {
			ServiceFiles(context.Context, bool, string) ([]string, error)
		}); ok {
			paths, e := files.ServiceFiles(ctx, user, name)
			if e != nil {
				return nil, e
			}
			for _, path := range paths {
				add(path, "restart", true)
			}
		}
		for _, mapping := range declaredMetadata(m, s, options.Profile, "configs") {
			_, target, e := configPaths(mapping, options)
			if e != nil {
				return nil, e
			}
			scope := "/systemd/system/"
			if user {
				scope = "/systemd/user/"
			}
			unit := name
			if !strings.Contains(unit, ".") {
				unit += ".service"
			}
			if strings.HasSuffix(target, scope+unit) || strings.Contains(target, scope+unit+".d/") || strings.HasSuffix(target, scope+unit+".d") {
				add(target, "restart", true)
				if strings.HasSuffix(target, scope+unit) {
					add(target+".d", "restart", true)
				}
			}
		}
		current, e := t.snapshot(service)
		if e != nil {
			return nil, e
		}
		record, exists := records[service]
		if !exists {
			record.Applied = current
		} else {
			if record.Applied == nil {
				record.Applied = map[string]string{}
			}
			// Adding a declaration establishes a baseline rather than inventing a change.
			for path, digest := range current {
				if _, ok := record.Applied[path]; !ok {
					record.Applied[path] = digest
				}
			}
		}
		records[service] = record
	}
	return t, nil
}

func dependencyPath(value string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	var expansionErr error
	path := os.Expand(value, func(key string) string {
		switch key {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return config
		}
		value, exists := os.LookupEnv(key)
		if !exists || value == "" {
			expansionErr = fmt.Errorf("dependency path environment variable %s is unset", key)
		}
		return value
	})
	if expansionErr != nil {
		return "", expansionErr
	}
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	return path, nil
}

func (t *ServiceTracker) snapshot(service string) (map[string]string, error) {
	result := map[string]string{}
	for key, input := range t.inputs[service] {
		digest, err := fingerprint(input.path, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("service %s dependency %s: %w", service, input.path, err)
		}
		result[key] = digest
	}
	return result, nil
}

// Follow symlink contents, including nested directory links, with cycle
// detection. Missing paths are distinct from empty files and directories.
func fingerprint(path string, ancestors map[string]bool) (string, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if ancestors[resolved] {
		return "", fmt.Errorf("dependency symlink cycle at %s", path)
	}
	ancestors[resolved] = true
	defer delete(ancestors, resolved)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", info.Mode().String())
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			digest, e := fingerprint(filepath.Join(path, entry.Name()), ancestors)
			if e != nil {
				return "", e
			}
			fmt.Fprintf(h, "%s\x00%s\x00", entry.Name(), digest)
		}
	} else {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("not a regular file or directory: %s", path)
		}
		f, e := os.Open(path)
		if e != nil {
			return "", e
		}
		_, e = io.Copy(h, f)
		closeErr := f.Close()
		if e != nil {
			return "", e
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PackageChanges records package changes that may affect services.
func (t *ServiceTracker) PackageChanges(names []string) {
	for service, deps := range t.packages {
		r := t.records[service]
		for _, name := range names {
			if contains(deps, name) {
				r.Packages = append(r.Packages, name)
			}
		}
		r.Packages = unique(r.Packages)
		t.records[service] = r
	}
}

// Actions returns the pending service actions.
func (t *ServiceTracker) Actions(s *state.State) ([]ServiceAction, error) {
	var actions []ServiceAction
	for _, service := range s.Items(state.ManagedKey, state.ManagedServices) {
		if _, exists := t.inputs[service]; !exists {
			continue
		}
		current, err := t.snapshot(service)
		if err != nil {
			return nil, err
		}
		r := t.records[service]
		a := ServiceAction{Service: service, Action: "reload"}
		for key, digest := range current {
			if r.Applied[key] != digest {
				a.Reasons = append(a.Reasons, key)
				input := t.inputs[service][key]
				if input.action == "restart" {
					a.Action = "restart"
				}
				a.DaemonReload = a.DaemonReload || input.unit
			}
		}
		for _, pkg := range r.Packages {
			a.Reasons = append(a.Reasons, "package:"+pkg)
			a.Action = "restart"
		}
		if len(a.Reasons) > 0 {
			sort.Strings(a.Reasons)
			actions = append(actions, a)
		}
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].Service < actions[j].Service })
	return actions, nil
}

func (t *ServiceTracker) save(s *state.State) error {
	managed := stringSet(s.Items(state.ManagedKey, state.ManagedServices))
	for name := range t.records {
		_, declared := t.inputs[name]
		_, owned := managed[name]
		if !declared || !owned {
			delete(t.records, name)
		}
	}
	s.SetServiceTracking(t.records)
	return s.Write()
}

// Checkpoint persists observations before resource mutations, so an interrupted
// run still detects the difference on its next invocation.
func (t *ServiceTracker) Checkpoint(s *state.State) error {
	records := map[string]state.ServiceTracking{}
	for _, name := range s.Items(state.ManagedKey, state.ManagedServices) {
		if record, ok := t.records[name]; ok {
			records[name] = record
		}
	}
	s.SetServiceTracking(records)
	return s.Write()
}

// Finish finalizes service tracking after reconciliation.
func (t *ServiceTracker) Finish(ctx context.Context, s *state.State, options Options, system System) error {
	if options.Output == nil {
		options.Output = os.Stdout
	}
	if options.Input == nil {
		options.Input = os.Stdin
	}
	actions, err := t.Actions(s)
	if err != nil {
		return &ServiceActionError{err}
	}
	if options.OutputFormat != "json" {
		for _, a := range actions {
			fmt.Fprintf(options.Output, "  %s %s: %s\n", a.Action, a.Service, strings.Join(a.Reasons, ", "))
		}
	}
	if options.DryRun {
		return nil
	}
	if err := t.save(s); err != nil {
		return &ServiceActionError{err}
	}
	approved := options.Yes || options.RestartServices
	if len(actions) > 0 && !approved && !options.NonInteractive && interactiveServiceInput(options.Input) {
		if _, ok := options.Input.(*bufio.Reader); !ok {
			options.Input = bufio.NewReader(options.Input)
		}
		approved, err = ask(options, "Apply affected service actions? [y/N] ")
		if err != nil {
			return &ServiceActionError{err}
		}
	}
	var failures []error
	reloaded := map[bool]error{}
	for _, a := range actions {
		user := strings.HasPrefix(a.Service, "user:")
		name := strings.TrimPrefix(a.Service, "user:")
		activity, ok := system.(interface {
			ServiceActive(context.Context, bool, string) (bool, error)
		})
		if !ok {
			failures = append(failures, fmt.Errorf("backend cannot inspect activity of %s", a.Service))
			continue
		}
		active, e := activity.ServiceActive(ctx, user, name)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if active && !approved {
			continue
		}
		// Capture before the command. Edits made during execution remain visible
		// on the next run rather than being silently marked applied.
		current, e := t.snapshot(a.Service)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if a.DaemonReload {
			if _, done := reloaded[user]; !done {
				reloaded[user] = system.ReloadServices(ctx, user)
			}
			if e = reloaded[user]; e != nil {
				failures = append(failures, e)
				continue
			}
		}
		if active {
			if a.Action == "reload" {
				reloader, ok := system.(interface {
					ReloadService(context.Context, bool, string) error
				})
				if !ok {
					e = fmt.Errorf("backend cannot reload %s", a.Service)
				} else {
					e = reloader.ReloadService(ctx, user, name)
				}
			} else {
				e = system.RestartService(ctx, user, name)
			}
		}
		if e != nil {
			failures = append(failures, fmt.Errorf("%s %s: %w", a.Action, a.Service, e))
			continue
		}
		t.records[a.Service] = state.ServiceTracking{Applied: current}
		if e = t.save(s); e != nil {
			return &ServiceActionError{e}
		}
	}
	if err := errors.Join(failures...); err != nil {
		return &ServiceActionError{err}
	}
	return nil
}

func interactiveServiceInput(input io.Reader) bool {
	if os.Getenv("DOTPKG_NONINTERACTIVE") == "1" {
		return false
	}
	// Reconciliation shares a buffered reader. Its terminal status is captured
	// separately by the caller; in-memory readers remain useful to embedders.
	if file, ok := input.(*os.File); ok {
		info, err := file.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0
	}
	return true
}
