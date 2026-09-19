package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
  --help             Show this help`)
}

type commonFlags struct {
	manifest string
	host     string
	state    string
	profile  string
	dryRun   bool
	yes      bool
	help     bool
	desktop  bool
	server   bool
	check    bool
}

func (f *commonFlags) register(set *flag.FlagSet) {
	set.StringVar(&f.manifest, "manifest", "packages.yaml", "package manifest")
	set.StringVar(&f.host, "host", "", "host overlay")
	set.StringVar(&f.state, "state-file", "", "state file")
	set.StringVar(&f.profile, "profile", "desktop", "profile")
	set.BoolVar(&f.dryRun, "dry-run", false, "do not apply changes")
	set.BoolVar(&f.check, "check", false, "alias for --dry-run")
	set.BoolVar(&f.yes, "yes", false, "skip confirmations")
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
		ManifestPath: f.manifest,
		HostPath:     f.host,
		StatePath:    f.state,
		Profile:      profile,
		DryRun:       f.dryRun || f.check,
		Yes:          f.yes,
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
	m, s, options, err := reconcile.Load(common.options())
	if err != nil {
		return err
	}
	if m.HasPackage(packageName) {
		return fmt.Errorf("package is already declared: %s", packageName)
	}
	selectedScope := *scope
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
	if err := m.AddPackage(target, path, packageName); err != nil {
		return err
	}
	_ = s
	return reconcile.Sync(ctx, options, system)
}

func chooseScope(options reconcile.Options, packageName string) (string, error) {
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
