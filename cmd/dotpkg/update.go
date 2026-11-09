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
	"strings"

	"github.com/lukelex/dotpkg/internal/backend"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/selfupdate"
)

var updateHostname = os.Hostname
var updateExecutable = os.Executable
var updateInput io.Reader = os.Stdin
var githubUpdater selfupdate.ReleaseUpdater = selfupdate.New()

func updateCommand(ctx context.Context, args []string, system backend.Backend) error {
	set := flag.NewFlagSet("update", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	var flags commonFlags
	flags.register(set)
	if err := set.Parse(args); err != nil {
		return err
	}
	if flags.help {
		fmt.Fprintln(os.Stdout, "Usage: dotpkg update [--manifest PATH] [--host NAME|PATH] [--dry-run] [--yes]")
		return nil
	}
	if set.NArg() != 0 {
		return fmt.Errorf("update accepts no positional arguments")
	}
	explicitHost := flags.host != ""
	if flags.host == "" {
		name, err := updateHostname()
		if err != nil {
			return fmt.Errorf("determine hostname for self-update: %w", err)
		}
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
			return fmt.Errorf("invalid hostname %q: supply --host explicitly", name)
		}
		flags.host = name
	}
	host := flags.host
	if _, err := os.Stat(host); errors.Is(err, os.ErrNotExist) {
		host = filepath.Join(filepath.Dir(flags.manifest), "hosts", flags.host+".yaml")
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(host); errors.Is(err, os.ErrNotExist) && !explicitHost {
		host = ""
	} else if err != nil {
		return fmt.Errorf("host overlay %s unavailable: %w", host, err)
	}
	// Validate the effective manifest before reading the explicit opt-in.
	if _, err := manifest.Load(flags.manifest, host); err != nil {
		return err
	}
	setting, err := manifest.SelfUpdateSetting(flags.manifest, host)
	if err != nil {
		return err
	}
	ctx, cancel := flags.commandContext(ctx)
	defer cancel()
	executable, err := updateExecutable()
	if err != nil {
		return fmt.Errorf("locate running executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve running executable: %w", err)
	}
	if setting.Origin == "github" {
		return updateGitHub(ctx, executable, flags)
	}
	updater, ok := system.(backend.SelfUpdater)
	if !ok {
		return fmt.Errorf("self-update is not supported by the selected package backend")
	}
	if err := flags.configureBackend(system); err != nil {
		return err
	}
	if err := updater.VerifyExecutable(ctx, executable, setting.Package); err != nil {
		return err
	}
	if !flags.dryRun && !flags.check {
		fmt.Fprintln(os.Stderr, "warning: updating only dotpkg on Arch can cause a partial upgrade; consider upgrading the full system first")
	}
	current, available, newer, err := updater.AvailableUpdate(ctx, setting.Package, setting.Origin, flags.dryRun || flags.check)
	if err != nil {
		return err
	}
	if flags.dryRun || flags.check {
		status := "up to date"
		if newer {
			status = "update available"
		}
		fmt.Printf("dry-run: %s installed %s, available %s (%s; repository metadata may be stale; no refresh or installation)\n", setting.Package, current, available, status)
		return nil
	}
	if !newer {
		fmt.Printf("%s is up to date (%s)\n", setting.Package, current)
		return nil
	}
	if !flags.yes {
		fmt.Printf("Upgrade %s from %s to %s? [y/N]: ", setting.Package, current, available)
		answer, err := bufio.NewReader(updateInput).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
			fmt.Println("Update cancelled")
			return nil
		}
	}
	if err := updater.UpgradePackage(ctx, setting.Package, setting.Origin); err != nil {
		return err
	}
	fmt.Printf("Updated %s from %s to %s\n", setting.Package, current, available)
	return nil
}

func updateGitHub(ctx context.Context, executable string, flags commonFlags) error {
	latest, newer, err := githubUpdater.Check(ctx, executable, version)
	if err != nil {
		return err
	}
	if flags.dryRun || flags.check {
		fmt.Printf("dry-run: dotpkg installed %s, available %s (update available: %t; no download or installation)\n", version, latest, newer)
		return nil
	}
	if !newer {
		fmt.Printf("dotpkg is up to date (%s)\n", version)
		return nil
	}
	if !flags.yes {
		fmt.Printf("Upgrade dotpkg from %s to %s using the GitHub release? [y/N]: ", version, latest)
		answer, err := bufio.NewReader(updateInput).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
			fmt.Println("Update cancelled")
			return nil
		}
	}
	if err := githubUpdater.Install(ctx, executable, latest); err != nil {
		return err
	}
	fmt.Printf("Updated dotpkg from %s to %s\n", version, latest)
	return nil
}
