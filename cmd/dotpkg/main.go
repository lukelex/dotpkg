package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lukelex/dotpkg/internal/backend"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/reconcile"
)

var version = "0.1.0-dev"

type stringList []string

func (items *stringList) String() string { return fmt.Sprint(*items) }

func (items *stringList) Set(value string) error {
	*items = append(*items, value)
	return nil
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "dotpkg: %s\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		usage(os.Stdout)
		return nil
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	system := backend.NewArch()
	switch args[0] {
	case "validate":
		return validate(ctx, args[1:], system)
	case "plan":
		return sync(ctx, args[1:], system, true)
	case "sync":
		return sync(ctx, args[1:], system, false)
	case "add":
		return add(ctx, args[1:], system)
	default:
		return fmt.Errorf("unknown command %q (try --help)", args[0])
	}
}

func usage(output *os.File) {
	fmt.Fprintln(output, `Usage: dotpkg COMMAND [options]

Commands:
  validate  Validate manifest packages against Arch repositories and the AUR.
  plan      Show package changes without modifying the system.
  sync      Reconcile declared packages and managed package state.
  add       Declare, validate, install, and track a package.
  version   Print the version.

Common options:
  --manifest PATH    Package manifest (default: packages.yaml)
  --host PATH        Host overlay to deep-merge over the manifest
  --state-file PATH  State file (default: $XDG_STATE_HOME/dotpkg/state.yaml)
  --profile NAME     desktop or server (default: desktop)
  --dry-run          Print changes without applying them
  --yes              Skip confirmation prompts
  --resources        Also reconcile groups, config links, and services
  --root PATH        Dotfiles root for resource paths
  --replace          Replace conflicting config targets
  --restart-services Restart services backed by config links
  --output FORMAT    Plan output: text or json (default: text)
  --help             Show this help`)
}

type commonFlags struct {
	manifest        string
	host            string
	state           string
	profile         string
	dryRun          bool
	yes             bool
	resources       bool
	root            string
	replace         bool
	restartServices bool
	output          string
	help            bool
	desktop         bool
	server          bool
	check           bool
}

func (f *commonFlags) register(set *flag.FlagSet) {
	set.StringVar(&f.manifest, "manifest", reconcile.DefaultManifestPath(), "package manifest")
	set.StringVar(&f.host, "host", "", "host overlay")
	set.StringVar(&f.state, "state-file", "", "state file")
	set.StringVar(&f.profile, "profile", "desktop", "profile")
	set.BoolVar(&f.dryRun, "dry-run", false, "do not apply changes")
	set.BoolVar(&f.check, "check", false, "alias for --dry-run")
	set.BoolVar(&f.yes, "yes", false, "skip confirmations")
	set.BoolVar(&f.resources, "resources", false, "reconcile groups, configs, and services")
	set.StringVar(&f.root, "root", "", "dotfiles root for resources")
	set.BoolVar(&f.replace, "replace", false, "replace conflicting config targets")
	set.BoolVar(&f.restartServices, "restart-services", false, "restart services backed by config links")
	set.StringVar(&f.output, "output", "text", "plan output format")
	set.BoolVar(&f.desktop, "desktop", false, "use the desktop profile")
	set.BoolVar(&f.server, "server", false, "use the server profile")
	set.BoolVar(&f.help, "help", false, "show help")
	set.BoolVar(&f.help, "h", false, "show help")
}

func (f commonFlags) options() reconcile.Options {
	profile := f.profile
	if f.desktop {
		profile = "desktop"
	}
	if f.server {
		profile = "server"
	}
	return reconcile.Options{
		ManifestPath:    f.manifest,
		HostPath:        f.host,
		StatePath:       f.state,
		Profile:         profile,
		DryRun:          f.dryRun || f.check,
		Yes:             f.yes,
		Resources:       f.resources,
		RootPath:        f.root,
		Replace:         f.replace,
		RestartServices: f.restartServices,
		OutputFormat:    f.output,
	}
}

func validate(ctx context.Context, args []string, system backend.Backend) error {
	set := flag.NewFlagSet("validate", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	var common commonFlags
	common.register(set)
	var extra stringList
	set.Var(&extra, "package", "also validate this package (repeatable)")
	if err := set.Parse(args); err != nil {
		return err
	}
	if common.help {
		usage(os.Stdout)
		return nil
	}
	m, _, options, err := reconcile.Load(common.options())
	if err != nil {
		return err
	}
	validation, err := reconcile.Validate(ctx, m, options.Profile, extra, system)
	if err != nil {
		return err
	}
	fmt.Printf("Validated %d manifest packages: %d repository, %d AUR\n", len(validation.Packages), len(validation.Repo), len(validation.AUR))
	if len(validation.Missing) > 0 {
		fmt.Fprintln(os.Stderr, "Missing packages:")
		for _, packageName := range validation.Missing {
			fmt.Fprintf(os.Stderr, "  %s\n", packageName)
		}
		return fmt.Errorf("manifest contains %d missing package(s)", len(validation.Missing))
	}
	return nil
}

func sync(ctx context.Context, args []string, system backend.Backend, planOnly bool) error {
	set := flag.NewFlagSet("sync", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	var common commonFlags
	common.register(set)
	if err := set.Parse(args); err != nil {
		return err
	}
	if common.help {
		usage(os.Stdout)
		return nil
	}
	if planOnly {
		common.dryRun = true
	}
	return reconcile.Sync(ctx, common.options(), system)
}

func add(ctx context.Context, args []string, system backend.Backend) error {
	// Keep compatibility with `package add NAME --scope ...` while accepting
	// the conventional flags-first form as well.
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		args = append(args[1:], args[0])
	}
	set := flag.NewFlagSet("add", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	var common commonFlags
	common.register(set)
	scope := set.String("scope", "", "manifest scope")
	if err := set.Parse(args); err != nil {
		return err
	}
	if common.help {
		usage(os.Stdout)
		return nil
	}
	if set.NArg() != 1 {
		return fmt.Errorf("add requires exactly one package name")
	}
	packageName := set.Arg(0)
	if !regexp.MustCompile(`^[A-Za-z0-9@._+:-]+$`).MatchString(packageName) {
		return fmt.Errorf("invalid package name: %s", packageName)
	}
	return reconcile.WithLock(common.options(), func(options reconcile.Options) error {
		return addLocked(ctx, packageName, *scope, options, system)
	})
}

func addLocked(ctx context.Context, packageName, requestedScope string, options reconcile.Options, system backend.Backend) error {
	m, s, options, err := reconcile.Load(options)
	if err != nil {
		return err
	}
	if m.HasPackage(packageName) {
		return fmt.Errorf("package is already declared: %s", packageName)
	}
	selectedScope := requestedScope
	if selectedScope == "" {
		selectedScope, err = chooseScope(options, packageName)
		if err != nil {
			return err
		}
	}
	path, err := scopePath(m, options.Profile, selectedScope)
	if err != nil {
		return err
	}
	if selectedScope == "hyprland" || selectedScope == "i3" || selectedScope == "extras" {
		if selected, recorded := s.Selection(selectedScope); !recorded || !selected {
			return fmt.Errorf("the %s scope is not selected locally; run sync and enable it first", selectedScope)
		}
	}
	if len(selectedScope) > len("option:") && selectedScope[:len("option:")] == "option:" {
		option := selectedScope[len("option:"):]
		if selected, recorded := s.Selection("options", option); !recorded || !selected {
			return fmt.Errorf("the %s scope is not selected locally; run sync and enable it first", selectedScope)
		}
	}
	validation, err := reconcile.Validate(ctx, m, options.Profile, []string{packageName}, system)
	if err != nil {
		return err
	}
	if len(validation.Missing) > 0 {
		return fmt.Errorf("package is not available: %s", packageName)
	}
	if options.DryRun {
		fmt.Fprintf(options.Output, "dry-run: add %s to %s in %s\n", packageName, selectedScope, options.ManifestPath)
		return nil
	}
	target := options.ManifestPath
	if options.HostPath != "" {
		target = options.HostPath
	}
	stagedTarget, err := stageFile(target)
	if err != nil {
		return err
	}
	defer os.Remove(stagedTarget)
	if err := m.AddPackage(stagedTarget, path, packageName); err != nil {
		return err
	}
	stateSnapshot, err := snapshotFile(options.StatePath)
	if err != nil {
		return err
	}
	syncOptions := options
	if options.HostPath != "" {
		syncOptions.HostPath = stagedTarget
	} else {
		syncOptions.ManifestPath = stagedTarget
	}
	if err := reconcile.SyncLocked(ctx, syncOptions, system); err != nil {
		if restoreErr := restoreFile(options.StatePath, stateSnapshot); restoreErr != nil {
			return fmt.Errorf("%w (restore state: %v)", err, restoreErr)
		}
		return err
	}
	if err := os.Rename(stagedTarget, target); err != nil {
		if restoreErr := restoreFile(options.StatePath, stateSnapshot); restoreErr != nil {
			return fmt.Errorf("replace manifest: %w (restore state: %v)", err, restoreErr)
		}
		return fmt.Errorf("replace manifest: %w", err)
	}
	_ = s
	return nil
}

type fileSnapshot struct {
	contents []byte
	mode     os.FileMode
	exists   bool
}

func snapshotFile(path string) (fileSnapshot, error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("read %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("stat %s: %w", path, err)
	}
	return fileSnapshot{contents: contents, mode: info.Mode().Perm(), exists: true}, nil
}

func stageFile(path string) (string, error) {
	snapshot, err := snapshotFile(path)
	if err != nil {
		return "", err
	}
	if !snapshot.exists {
		return "", fmt.Errorf("file does not exist: %s", path)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".dotpkg-*")
	if err != nil {
		return "", fmt.Errorf("stage %s: %w", path, err)
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		temporary.Close()
		os.Remove(temporaryName)
	}
	if err := temporary.Chmod(snapshot.mode); err != nil {
		cleanup()
		return "", fmt.Errorf("set staged permissions: %w", err)
	}
	if _, err := temporary.Write(snapshot.contents); err != nil {
		cleanup()
		return "", fmt.Errorf("stage %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryName)
		return "", fmt.Errorf("close staged %s: %w", path, err)
	}
	return temporaryName, nil
}

func restoreFile(path string, snapshot fileSnapshot) error {
	if !snapshot.exists {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".dotpkg-restore-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		temporary.Close()
		os.Remove(temporaryName)
	}
	if err := temporary.Chmod(snapshot.mode); err != nil {
		cleanup()
		return err
	}
	if _, err := temporary.Write(snapshot.contents); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryName)
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		os.Remove(temporaryName)
		return err
	}
	return nil
}

func chooseScope(options reconcile.Options, packageName string) (string, error) {
	if options.Yes {
		return options.Profile, nil
	}
	if options.DryRun {
		fmt.Fprintf(options.Output, "dry-run: default package scope is %s\n", options.Profile)
		return options.Profile, nil
	}

	if options.Profile == "desktop" {
		fmt.Fprintln(options.Output, "Available scopes: desktop, hyprland, i3, extras, common, option:NAME")
	} else {
		fmt.Fprintln(options.Output, "Available scopes: server, common")
	}
	fmt.Fprintf(options.Output, "Add %s to scope [%s]: ", packageName, options.Profile)
	answer, err := bufio.NewReader(options.Input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return options.Profile, nil
	}
	return answer, nil
}

func scopePath(m *manifest.Manifest, profile, scope string) (string, error) {
	if scope == "" {
		scope = profile
	}
	switch scope {
	case "common":
		return "common.packages.headless", nil
	case "desktop":
		if profile != "desktop" {
			return "", fmt.Errorf("the desktop scope requires --profile desktop")
		}
		return "profiles.desktop.packages.desktop", nil
	case "server":
		if profile != "server" {
			return "", fmt.Errorf("the server scope requires --profile server")
		}
		return "profiles.server.packages.headless", nil
	case "hyprland", "i3", "extras":
		if profile != "desktop" {
			return "", fmt.Errorf("the %s scope requires --profile desktop", scope)
		}
		return "profiles.desktop.packages." + scope, nil
	default:
		if len(scope) > len("option:") && scope[:len("option:")] == "option:" {
			if profile != "desktop" {
				return "", fmt.Errorf("optional scopes require --profile desktop")
			}
			option := scope[len("option:"):]
			if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(option) {
				return "", fmt.Errorf("invalid option scope: %s", scope)
			}
			if m.Value("profiles.desktop.options."+option+".packages") == nil {
				return "", fmt.Errorf("unknown optional scope: %s", scope)
			}
			return "profiles.desktop.options." + option + ".packages", nil
		}
		return "", fmt.Errorf("unknown scope: %s", scope)
	}
}
