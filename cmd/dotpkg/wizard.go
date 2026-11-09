package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/lukelex/dotpkg/internal/reconcile"
	"github.com/lukelex/dotpkg/internal/wizard"
)

func wizardCommand(ctx context.Context, args []string) error {
	set := flag.NewFlagSet("wizard", flag.ContinueOnError)
	var common commonFlags
	common.register(set)
	var options wizard.Options
	set.BoolVar(&options.Plain, "plain", false, "use explicit unattended execution")
	set.BoolVar(&options.Resume, "resume", false, "resume saved onboarding preferences")
	set.BoolVar(&options.PackagesOnly, "packages-only", false, "run only package setup steps")
	set.StringVar(&options.SelectionsPath, "selections", "", "JSON file of explicit package selections")
	if err := set.Parse(args); err != nil {
		return err
	}
	if common.help {
		fmt.Fprintln(os.Stdout, "Usage: dotpkg wizard [--manifest PATH] [--root PATH] [--resume] [--dry-run] [--plain --yes --selections PATH]\n\nWithout a default manifest, offers a starter setup for an installed Arch Linux machine.\nCustom manifests use onboarding.version: 1 to define the workflow.\n--dry-run is read-only and does not execute manifest commands.")
		set.PrintDefaults()
		return nil
	}
	if set.NArg() != 0 {
		return fmt.Errorf("wizard does not accept positional arguments")
	}
	options.Reconcile = common.options()
	explicitManifest := os.Getenv("DOTPKG_MANIFEST") != ""
	explicitSession := false
	set.Visit(func(f *flag.Flag) {
		if f.Name == "manifest" {
			explicitManifest = true
		}
		switch f.Name {
		case "resume", "profile", "desktop", "server", "host":
			explicitSession = true
		}
	})
	options.AutoResume = !explicitManifest && !explicitSession
	if !explicitManifest {
		if _, err := os.Lstat(common.manifest); os.IsNotExist(err) {
			options.Bootstrap = true
			options.Reconcile.ManifestPath = reconcile.ConfigManifestPath()
		}
	}
	options.Yes = common.yes
	return wizard.Run(ctx, options)
}
