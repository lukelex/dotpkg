package backend

import (
	"context"
	"fmt"
	"strings"
)

type Debian struct {
	nativeResources
}

func NewDebian() *Debian {
	return &Debian{nativeResources: nativeResources{Runner: OSRunner{}}}
}

func (d *Debian) IsInstalled(ctx context.Context, packageName string) (bool, error) {
	if _, err := d.Runner.LookPath("dpkg-query"); err != nil {
		return false, fmt.Errorf("dpkg-query is required: %w", err)
	}
	output, err := d.Runner.Output(ctx, "dpkg-query", []string{"-W", "-f=${Status}", packageName})
	if err == nil {
		return strings.Contains(string(output), "install ok installed"), nil
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check installed package %s: %w", packageName, err)
}

func (d *Debian) RepositoryPackages(ctx context.Context) (map[string]struct{}, error) {
	if _, err := d.Runner.LookPath("apt-cache"); err != nil {
		return nil, fmt.Errorf("apt-cache is required: %w", err)
	}
	output, err := d.Runner.Output(ctx, "apt-cache", []string{"pkgnames"})
	if err != nil {
		return nil, fmt.Errorf("list Debian packages: %w", err)
	}
	return linesToSet(string(output)), nil
}

func (d *Debian) AURPackages(_ context.Context, names []string) (map[string]struct{}, error) {
	if len(names) == 0 {
		return map[string]struct{}{}, nil
	}
	return nil, fmt.Errorf("AUR packages are unsupported by the Debian backend: %s", strings.Join(names, ", "))
}

func (d *Debian) Install(ctx context.Context, repository, aur []string, _ string) error {
	if len(aur) > 0 {
		return fmt.Errorf("cannot install AUR packages with the Debian backend: %s", strings.Join(aur, ", "))
	}
	if len(repository) == 0 {
		return nil
	}
	if err := d.Runner.Run(ctx, "sudo", append([]string{"apt-get", "install", "-y"}, repository...), ""); err != nil {
		return fmt.Errorf("install Debian packages: %w", err)
	}
	return nil
}

func (d *Debian) Remove(ctx context.Context, packages []string, _ string) error {
	if len(packages) == 0 {
		return nil
	}
	if err := d.Runner.Run(ctx, "sudo", append([]string{"apt-get", "purge", "-y"}, packages...), ""); err != nil {
		return fmt.Errorf("remove Debian packages: %w", err)
	}
	return nil
}

var _ Backend = (*Debian)(nil)
