package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Arch struct {
	HTTPClient *http.Client
	Runner     Runner
}

// Runner makes command execution replaceable in tests without weakening the
// production boundary, which always passes argv directly to exec.Command.
type Runner interface {
	LookPath(string) (string, error)
	Output(context.Context, string, []string) ([]byte, error)
	Run(context.Context, string, []string, string) error
}

type OSRunner struct{}

func (OSRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (OSRunner) Output(ctx context.Context, name string, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (OSRunner) Run(ctx context.Context, name string, args []string, directory string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func NewArch() *Arch {
	return &Arch{HTTPClient: http.DefaultClient, Runner: OSRunner{}}
}

func (a *Arch) IsInstalled(ctx context.Context, packageName string) (bool, error) {
	if _, err := a.Runner.LookPath("pacman"); err != nil {
		return false, fmt.Errorf("pacman is required: %w", err)
	}
	_, err := a.Runner.Output(ctx, "pacman", []string{"-Q", packageName})
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check installed package %s: %w", packageName, err)
}

func (a *Arch) RepositoryPackages(ctx context.Context) (map[string]struct{}, error) {
	if _, err := a.Runner.LookPath("pacman"); err != nil {
		return nil, fmt.Errorf("pacman is required: %w", err)
	}
	output, err := a.Runner.Output(ctx, "pacman", []string{"-Slq"})
	if err != nil {
		return nil, fmt.Errorf("list repository packages: %w", err)
	}
	return linesToSet(string(output)), nil
}

type aurResponse struct {
	Results []struct {
		Name string
	} `json:"results"`
}

func (a *Arch) AURPackages(ctx context.Context, names []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(names) == 0 {
		return result, nil
	}
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	for start := 0; start < len(names); start += 200 {
		end := start + 200
		if end > len(names) {
			end = len(names)
		}
		query := url.Values{}
		for _, name := range names[start:end] {
			query.Add("arg[]", name)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://aur.archlinux.org/rpc/v5/info?"+query.Encode(), nil)
		if err != nil {
			return nil, fmt.Errorf("create AUR request: %w", err)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("AUR request failed: %w", err)
		}
		if response.Body == nil {
			return nil, fmt.Errorf("AUR response has no body")
		}
		var payload aurResponse
		decodeError := json.NewDecoder(response.Body).Decode(&payload)
		closeError := response.Body.Close()
		if decodeError != nil {
			return nil, fmt.Errorf("decode AUR response: %w", decodeError)
		}
		if closeError != nil {
			return nil, fmt.Errorf("close AUR response: %w", closeError)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("AUR returned HTTP %s", response.Status)
		}
		for _, packageInfo := range payload.Results {
			result[packageInfo.Name] = struct{}{}
		}
	}
	return result, nil
}

func (a *Arch) Install(ctx context.Context, repository, aur []string, profile string) error {
	if len(repository) > 0 {
		if err := a.Runner.Run(ctx, "sudo", append([]string{"pacman", "-S", "--needed", "--noconfirm"}, repository...), ""); err != nil {
			return fmt.Errorf("install repository packages: %w", err)
		}
	}
	if len(aur) == 0 {
		return nil
	}
	if _, err := a.Runner.LookPath("yay"); err != nil {
		if err := a.bootstrapYay(ctx); err != nil {
			return err
		}
	}
	if err := a.Runner.Run(ctx, "yay", append([]string{"-S", "--needed", "--noconfirm"}, aur...), ""); err != nil {
		return fmt.Errorf("install AUR packages: %w", err)
	}
	return nil
}

func (a *Arch) Remove(ctx context.Context, packages []string, profile string) error {
	if len(packages) == 0 {
		return nil
	}
	if profile == "server" {
		if err := a.Runner.Run(ctx, "sudo", append([]string{"pacman", "-Rns", "--noconfirm"}, packages...), ""); err != nil {
			return fmt.Errorf("remove packages: %w", err)
		}
		return nil
	}
	if _, err := a.Runner.LookPath("yay"); err != nil {
		return fmt.Errorf("yay is required to remove desktop packages: %w", err)
	}
	if err := a.Runner.Run(ctx, "yay", append([]string{"-Rns", "--noconfirm"}, packages...), ""); err != nil {
		return fmt.Errorf("remove packages: %w", err)
	}
	return nil
}

func (a *Arch) bootstrapYay(ctx context.Context) error {
	if _, err := a.Runner.LookPath("git"); err != nil {
		return fmt.Errorf("git is required to bootstrap yay: %w", err)
	}
	if _, err := a.Runner.LookPath("makepkg"); err != nil {
		return fmt.Errorf("makepkg is required to bootstrap yay: %w", err)
	}
	directory, err := os.MkdirTemp("", "dotpkg-yay-")
	if err != nil {
		return fmt.Errorf("create yay build directory: %w", err)
	}
	defer os.RemoveAll(directory)
	checkout := filepath.Join(directory, "yay-git")
	if err := a.Runner.Run(ctx, "git", []string{"clone", "https://aur.archlinux.org/yay-git.git", checkout}, ""); err != nil {
		return fmt.Errorf("clone yay: %w", err)
	}
	if err := a.Runner.Run(ctx, "makepkg", []string{"-si"}, checkout); err != nil {
		return fmt.Errorf("build yay: %w", err)
	}
	return nil
}

func linesToSet(output string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			result[name] = struct{}{}
		}
	}
	return result
}
