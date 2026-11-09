package backend

import (
	"context"
	"fmt"
	"strings"
)

// SelfUpdater is optional; package reconciliation does not depend on upgrades.
type SelfUpdater interface {
	VerifyExecutable(context.Context, string, string) error
	AvailableUpdate(context.Context, string, string, bool) (string, string, bool, error)
	UpgradePackage(context.Context, string, string) error
}

func (a *Arch) VerifyExecutable(ctx context.Context, executable, packageName string) error {
	if _, err := a.Runner.LookPath("pacman"); err != nil {
		return fmt.Errorf("pacman is required: %w", err)
	}
	owner, err := a.Runner.Output(ctx, "pacman", []string{"-Qoq", "--", executable})
	if err != nil {
		return fmt.Errorf("running executable %s is not owned by package %s (standalone installs cannot self-update): %w", executable, packageName, err)
	}
	if strings.TrimSpace(string(owner)) != packageName {
		return fmt.Errorf("running executable %s is owned by %q, not configured package %q", executable, strings.TrimSpace(string(owner)), packageName)
	}
	return nil
}

func (a *Arch) AvailableUpdate(ctx context.Context, packageName, origin string, dryRun bool) (string, string, bool, error) {
	if origin != "repo" && origin != "aur" {
		return "", "", false, fmt.Errorf("unsupported self-update origin %q", origin)
	}
	if origin == "aur" {
		if _, err := a.Runner.LookPath("yay"); err != nil {
			return "", "", false, fmt.Errorf("yay is required for AUR self-update: %w", err)
		}
	}
	if _, err := a.Runner.LookPath("vercmp"); err != nil {
		return "", "", false, fmt.Errorf("vercmp is required to compare package versions: %w", err)
	}
	installed, err := a.Runner.Output(ctx, "pacman", []string{"-Q", packageName})
	if err != nil {
		return "", "", false, fmt.Errorf("configured self-update package %s is not installed: %w", packageName, err)
	}
	parts := strings.Fields(strings.TrimSpace(string(installed)))
	if len(parts) != 2 || parts[0] != packageName {
		return "", "", false, fmt.Errorf("unexpected installed version for %s: %q", packageName, strings.TrimSpace(string(installed)))
	}
	current := parts[1]
	query := "-Qn"
	if origin == "aur" {
		query = "-Qm"
	}
	if _, err := a.Runner.Output(ctx, "pacman", []string{query, packageName}); err != nil {
		return "", "", false, fmt.Errorf("installed package %s does not match configured %s origin: %w", packageName, origin, err)
	}
	if origin == "repo" && !dryRun {
		if err := a.Runner.Run(ctx, "sudo", []string{"pacman", "-Sy", "--noconfirm"}, ""); err != nil {
			return "", "", false, fmt.Errorf("refresh repository metadata: %w", err)
		}
	}
	manager, args := "pacman", []string{"-Si", packageName}
	if origin == "aur" {
		manager, args = "yay", []string{"--aur", "-Si", packageName}
	}
	info, err := a.Runner.Output(ctx, manager, args)
	if err != nil {
		return "", "", false, fmt.Errorf("query %s version of %s: %w", origin, packageName, err)
	}
	var available string
	for _, line := range strings.Split(string(info), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "Version" {
			available = strings.TrimSpace(value)
			break
		}
	}
	if available == "" || strings.ContainsAny(available, " \t\r\n") {
		return "", "", false, fmt.Errorf("missing or invalid version in %s package information for %s", manager, packageName)
	}
	comparison, err := a.Runner.Output(ctx, "vercmp", []string{available, current})
	if err != nil {
		return "", "", false, fmt.Errorf("compare versions for %s: %w", packageName, err)
	}
	switch strings.TrimSpace(string(comparison)) {
	case "1":
		return current, available, true, nil
	case "0", "-1":
		return current, available, false, nil
	default:
		return "", "", false, fmt.Errorf("unexpected vercmp result %q", strings.TrimSpace(string(comparison)))
	}
}

func (a *Arch) UpgradePackage(ctx context.Context, packageName, origin string) error {
	switch origin {
	case "repo":
		if err := a.Runner.Run(ctx, "sudo", []string{"pacman", "-S", "--needed", "--noconfirm", packageName}, ""); err != nil {
			return fmt.Errorf("upgrade repository package %s: %w", packageName, err)
		}
	case "aur":
		if err := a.Runner.Run(ctx, "yay", []string{"--aur", "-S", "--needed", "--noconfirm", packageName}, ""); err != nil {
			return fmt.Errorf("upgrade AUR package %s: %w", packageName, err)
		}
	default:
		return fmt.Errorf("unsupported self-update origin %q", origin)
	}
	return nil
}
