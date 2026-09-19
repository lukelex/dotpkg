package main

import (
	"strings"
	"testing"

	"github.com/lukelex/dotpkg/internal/reconcile"
)

func TestChooseScopeUsesProfileByDefaultInDryRun(t *testing.T) {
	var output strings.Builder
	options := reconcile.Options{
		Profile: "server",
		DryRun:  true,
		Output:  &output,
	}

	scope, err := chooseScope(options, "ripgrep")
	if err != nil {
		t.Fatal(err)
	}
	if scope != "server" {
		t.Fatalf("scope = %q, want server", scope)
	}
	if got := output.String(); got != "dry-run: default package scope is server\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestChooseScopePromptsAndAcceptsExplicitScope(t *testing.T) {
	var output strings.Builder
	options := reconcile.Options{
		Profile: "desktop",
		Input:   strings.NewReader("common\n"),
		Output:  &output,
	}

	scope, err := chooseScope(options, "ripgrep")
	if err != nil {
		t.Fatal(err)
	}
	if scope != "common" {
		t.Fatalf("scope = %q, want common", scope)
	}
	if got := output.String(); got != "Available scopes: desktop, hyprland, i3, extras, common, option:NAME\nAdd ripgrep to scope [desktop]: " {
		t.Fatalf("output = %q", got)
	}
}

func TestChooseScopeDefaultsAfterEmptyAnswer(t *testing.T) {
	options := reconcile.Options{
		Profile: "desktop",
		Input:   strings.NewReader("\n"),
		Output:  &strings.Builder{},
	}

	scope, err := chooseScope(options, "ripgrep")
	if err != nil {
		t.Fatal(err)
	}
	if scope != "desktop" {
		t.Fatalf("scope = %q, want desktop", scope)
	}
}

func TestScopePathSupportsCommonForBothProfiles(t *testing.T) {
	for _, profile := range []string{"desktop", "server"} {
		path, err := scopePath(nil, profile, "common")
		if err != nil {
			t.Fatalf("profile %s: %v", profile, err)
		}
		if path != "common.packages.headless" {
			t.Fatalf("profile %s: path = %q", profile, path)
		}
	}
}

func TestScopePathRejectsInvalidOptionName(t *testing.T) {
	if _, err := scopePath(nil, "desktop", "option:bad.name"); err == nil {
		t.Fatal("invalid option scope was accepted")
	}
}
