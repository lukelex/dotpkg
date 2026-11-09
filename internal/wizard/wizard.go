package wizard

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/lukelex/dotpkg/internal/reconcile"
	"github.com/lukelex/dotpkg/internal/resource"
)

// ErrIncomplete indicates an unfinished wizard workflow.
var ErrIncomplete = errors.New("setup remains unfinished; rerun the wizard to continue")

// Run runs the requested operation.
func Run(ctx context.Context, options Options) error {
	r, choices, err := prepare(options)
	if err != nil {
		return err
	}
	if options.Reconcile.DryRun {
		fmt.Printf("%s — read-only preview\n%s\n", r.spec.Title, r.spec.Description)
		for _, step := range r.spec.ForProfile(r.session.Profile, options.PackagesOnly) {
			fmt.Printf("\n%s\n  %s\n", step.Title, step.Description)
		}
		fmt.Println("\nNo checks, actions, downloads, or state writes performed. Package transactions are resolved during execution.")
		return nil
	}
	if !options.Plain && (!term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd())) {
		return fmt.Errorf("wizard requires a terminal; use --plain --yes with explicit selections and inputs, or --dry-run")
	}
	// Prevent two login/manual wizards racing on session state and system work.
	if err := os.MkdirAll(r.directory, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(r.directory, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another wizard is already running: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	defer func() {
		if r.log != nil {
			r.log.Close()
		}
		if r.events != nil {
			r.events.Close()
		}
	}()
	if options.Plain {
		return r.runPlain(choices)
	}
	m := newModel(r, choices)
	result, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err != nil {
		return err
	}
	final := result.(*model)
	if final.err != nil {
		return final.err
	}
	if !final.complete {
		return ErrIncomplete
	}
	fmt.Printf("Setup complete. Logs: %s\n", filepath.Join(r.directory, "run.log"))
	return nil
}

func prepare(options Options) (*runtime, []reconcile.SelectionChoice, error) {
	var err error
	options.Reconcile.ManifestPath, err = filepath.Abs(options.Reconcile.ManifestPath)
	if err != nil {
		return nil, nil, err
	}
	if options.Reconcile.RootPath == "" {
		options.Reconcile.RootPath = filepath.Dir(options.Reconcile.ManifestPath)
	}
	options.Reconcile.RootPath, err = filepath.Abs(options.Reconcile.RootPath)
	if err != nil {
		return nil, nil, err
	}
	if options.Bootstrap && options.Reconcile.StatePath == "" {
		options.Reconcile.StatePath = reconcile.ConfigStatePath()
	}
	m, s, normalized, err := loadManifest(options)
	if err != nil {
		return nil, nil, err
	}
	options.Reconcile = normalized
	options.Reconcile.StatePath, err = filepath.Abs(normalized.StatePath)
	if err != nil {
		return nil, nil, err
	}
	spec, err := Load(m)
	if err != nil {
		return nil, nil, err
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, _ := os.UserHomeDir()
		stateHome = filepath.Join(home, ".local", "state")
	}
	r := &runtime{options: options, spec: spec, directory: filepath.Join(stateHome, "dotpkg", fmt.Sprintf("wizard-%x", sha256.Sum256([]byte(normalized.ManifestPath))))}
	r.executable, err = os.Executable()
	if err != nil {
		return nil, nil, err
	}
	r.session = Session{Selections: map[string]bool{}, Inputs: map[string]string{}, Deferred: map[string]bool{}, Confirmed: map[string]bool{}}
	if data, err := os.ReadFile(r.sessionPath()); err == nil {
		if err := json.Unmarshal(data, &r.session); err != nil {
			return nil, nil, fmt.Errorf("read wizard session: %w", err)
		}
		if options.AutoResume {
			options.Resume = true
			r.options.Resume = true
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	if r.session.Selections == nil {
		r.session.Selections = map[string]bool{}
	}
	if r.session.Inputs == nil {
		r.session.Inputs = map[string]string{}
	}
	if r.session.Deferred == nil {
		r.session.Deferred = map[string]bool{}
	}
	if r.session.Confirmed == nil {
		r.session.Confirmed = map[string]bool{}
	}
	if !options.Resume || r.session.Profile == "" {
		r.session.Profile = normalized.Profile
		r.session.Host = normalized.HostLabel
	}
	if options.Resume {
		r.options.Reconcile.Profile = r.session.Profile
		r.options.Reconcile.HostPath = r.session.Host
		m, s, _, err = loadManifest(r.options)
		if err != nil {
			return nil, nil, err
		}
		r.spec, err = Load(m)
		if err != nil {
			return nil, nil, err
		}
		spec = r.spec
	}
	if options.SelectionsPath != "" {
		data, err := os.ReadFile(options.SelectionsPath)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(data, &r.session.Selections); err != nil {
			return nil, nil, err
		}
	}
	choices := reconcile.SelectionChoices(m, s, r.session.Profile)
	if r.session.Profile == "server" {
		r.session.Selections = map[string]bool{}
	}
	for i := range choices {
		if value, ok := r.session.Selections[choices[i].Key]; ok {
			choices[i].Selected = value
			choices[i].Recorded = true
		}
		if options.Resume {
			r.session.Selections[choices[i].Key] = choices[i].Selected
		}
	}
	// Defaults are inspected only for interactive runs, never for previews.
	for _, input := range spec.Inputs {
		if value, ok := os.LookupEnv("DOTPKG_INPUT_" + strings.ToUpper(strings.ReplaceAll(input.ID, "-", "_"))); ok {
			r.session.Inputs[input.ID] = value
			continue
		}
		if _, ok := r.session.Inputs[input.ID]; !ok && !options.Plain {
			value := input.Default
			if !options.Reconcile.DryRun && len(input.DefaultCommand) > 0 {
				if found, err := r.inspect(input.DefaultCommand); err == nil && found != "" {
					value = found
				}
			}
			r.session.Inputs[input.ID] = value
		}
	}
	return r, choices, nil
}

func (r *runtime) reloadChoices() ([]reconcile.SelectionChoice, error) {
	options := r.options.Reconcile
	options.Profile, options.HostPath = r.session.Profile, r.session.Host
	loadOptions := r.options
	loadOptions.Reconcile = options
	m, s, _, err := loadManifest(loadOptions)
	if err != nil {
		return nil, err
	}
	spec, err := Load(m)
	if err != nil {
		return nil, err
	}
	r.spec = spec
	choices := reconcile.SelectionChoices(m, s, r.session.Profile)
	for i := range choices {
		if v, ok := r.session.Selections[choices[i].Key]; ok {
			choices[i].Selected = v
			choices[i].Recorded = true
		}
	}
	return choices, nil
}

func (r *runtime) conflicts() ([]resource.ConfigConflict, error) {
	options := r.options.Reconcile
	options.Profile, options.HostPath = r.session.Profile, r.session.Host
	options.Selections, options.RequireSelections = r.session.Selections, true
	m, s, _, err := reconcile.Load(options)
	if err != nil {
		return nil, err
	}
	if _, err := reconcile.EnsureSelections(m, s, options); err != nil {
		return nil, err
	}
	return resource.ConfigConflicts(m, s, resource.Options{Profile: r.session.Profile, RootPath: options.RootPath})
}

func (r *runtime) hosts() []string {
	hosts := []string{""}
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(r.options.Reconcile.ManifestPath), "hosts", "*.yaml"))
	for _, path := range paths {
		hosts = append(hosts, strings.TrimSuffix(filepath.Base(path), ".yaml"))
	}
	if r.session.Host != "" {
		found := false
		for _, host := range hosts {
			found = found || host == r.session.Host
		}
		if !found {
			hosts = append(hosts, r.session.Host)
		}
	}
	sort.Strings(hosts)
	return hosts
}

func (r *runtime) runPlain(choices []reconcile.SelectionChoice) error {
	if !r.options.Yes {
		return fmt.Errorf("plain execution requires --yes; use --dry-run to preview")
	}
	for _, choice := range choices {
		if !choice.Recorded {
			return fmt.Errorf("unresolved selection %s; provide --selections", choice.Key)
		}
		r.session.Selections[choice.Key] = choice.Selected
	}
	for _, input := range r.spec.Inputs {
		if _, ok := r.session.Inputs[input.ID]; !ok {
			return fmt.Errorf("unresolved input %s; supply DOTPKG_INPUT_%s", input.ID, strings.ToUpper(input.ID))
		}
	}
	steps := r.spec.ForProfile(r.session.Profile, r.options.PackagesOnly)
	// Refuse before starting if unattended operation would need terminal access.
	for _, step := range steps {
		if r.session.Deferred[step.ID] {
			continue
		}
		if len(step.Check) > 0 {
			if _, err := r.inspect(step.Check); err == nil {
				continue
			}
		}
		if (step.Interactive && len(step.UnattendedAction) == 0) || step.Manual || step.Graphical || step.Kind != "" {
			return fmt.Errorf("step %s requires the interactive wizard", step.ID)
		}
	}
	if err := r.begin(); err != nil {
		return err
	}
	for _, step := range steps {
		fmt.Printf("\n%s\n%s\n", step.Title, step.Description)
		if r.session.Deferred[step.ID] {
			fmt.Println("Deferred")
			continue
		}
		if len(step.Check) > 0 {
			if _, err := r.inspect(step.Check); err == nil {
				fmt.Println("Already ready")
				continue
			}
		}
		if len(step.Action) == 0 {
			return fmt.Errorf("%s needs external setup: %s", step.Title, step.Instructions)
		}
		r.event(step.ID, "running")
		action := step.Action
		if len(step.UnattendedAction) > 0 {
			action = step.UnattendedAction
		}
		cmd := r.command(context.Background(), action)
		cmd.Stdout = r
		cmd.Stderr = r
		if err := cmd.Run(); err != nil {
			r.event(step.ID, "failed")
			return fmt.Errorf("%s: %w (logs: %s)", step.Title, err, r.directory)
		}
		if len(step.Check) > 0 {
			if _, err := r.inspect(step.Check); err != nil {
				return fmt.Errorf("%s still needs setup: %s", step.Title, step.Instructions)
			}
		}
		r.event(step.ID, "done")
	}
	return r.continuation(false)
}
