package backend

import (
	"context"
	"fmt"
	"strings"
)

type Fedora struct {
	nativeResources
}

func NewFedora() *Fedora {
	return &Fedora{nativeResources: nativeResources{Runner: OSRunner{}}}
}

func (f *Fedora) IsInstalled(ctx context.Context, packageName string) (bool, error) {
	if _, err := f.Runner.LookPath("rpm"); err != nil {
		return false, fmt.Errorf("rpm is required: %w", err)
	}
	_, err := f.Runner.Output(ctx, "rpm", []string{"-q", packageName})
	if err == nil {
		return true, nil
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check installed package %s: %w", packageName, err)
}

func (f *Fedora) RepositoryPackages(ctx context.Context) (map[string]struct{}, error) {
	if _, err := f.Runner.LookPath("dnf"); err != nil {
		return nil, fmt.Errorf("dnf is required: %w", err)
	}
	output, err := f.Runner.Output(ctx, "dnf", []string{"repoquery", "--qf", "%{name}"})
	if err != nil {
		return nil, fmt.Errorf("list Fedora packages: %w", err)
	}
	return linesToSet(string(output)), nil
}

func (f *Fedora) AURPackages(_ context.Context, names []string) (map[string]struct{}, error) {
	if len(names) == 0 {
		return map[string]struct{}{}, nil
	}
	return nil, fmt.Errorf("AUR packages are unsupported by the Fedora backend: %s", strings.Join(names, ", "))
}

func (f *Fedora) Install(ctx context.Context, repository, aur []string, _ string) error {
	if len(aur) > 0 {
		return fmt.Errorf("cannot install AUR packages with the Fedora backend: %s", strings.Join(aur, ", "))
	}
	if len(repository) == 0 {
		return nil
	}
	if err := f.Runner.Run(ctx, "sudo", append([]string{"dnf", "install", "-y"}, repository...), ""); err != nil {
		return fmt.Errorf("install Fedora packages: %w", err)
	}
	return nil
}

func (f *Fedora) Remove(ctx context.Context, packages []string, _ string) error {
	if len(packages) == 0 {
		return nil
	}
	if err := f.Runner.Run(ctx, "sudo", append([]string{"dnf", "remove", "-y"}, packages...), ""); err != nil {
		return fmt.Errorf("remove Fedora packages: %w", err)
	}
	return nil
}

var _ Backend = (*Fedora)(nil)
