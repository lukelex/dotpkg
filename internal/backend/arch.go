// Package backend integrates with system package managers and services.
package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Arch implements package and service operations on Arch Linux.
type Arch struct {
	HTTPClient    *http.Client
	Runner        Runner
	AURRetries    int
	AURRetryDelay time.Duration
	Logger        io.Writer
}

const yayBootstrapRevision = "4cf171c209a9ef4dc0f9e0c80f4e3b9900aeea90"

// Runner makes command execution replaceable in tests without weakening the
// production boundary, which always passes argv directly to exec.Command.
type Runner interface {
	LookPath(string) (string, error)
	Output(context.Context, string, []string) ([]byte, error)
	Run(context.Context, string, []string, string) error
}

// OSRunner executes external commands using the host operating system.
type OSRunner struct{}

// LookPath finds an executable on the host.
func (OSRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// Output runs a command and returns its output.
func (OSRunner) Output(ctx context.Context, name string, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Run runs the requested operation.
func (OSRunner) Run(ctx context.Context, name string, args []string, directory string) error {
	if os.Getenv("DOTPKG_NONINTERACTIVE") == "1" && name == "sudo" {
		args = append([]string{"-n"}, args...)
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

// NewArch creates a new package-manager backend.
func NewArch() *Arch {
	return &Arch{
		HTTPClient:    &http.Client{Timeout: 30 * time.Second},
		Runner:        OSRunner{},
		AURRetries:    3,
		AURRetryDelay: 100 * time.Millisecond,
		Logger:        io.Discard,
	}
}

// Configure sets the package-manager command options.
func (a *Arch) Configure(options Options) error {
	if options.HTTPTimeout < 0 {
		return fmt.Errorf("HTTP timeout cannot be negative")
	}
	if options.AURRetries < 1 {
		return fmt.Errorf("AUR retries must be at least 1")
	}
	if options.AURRetryDelay < 0 {
		return fmt.Errorf("AUR retry delay cannot be negative")
	}
	if a.HTTPClient == nil {
		a.HTTPClient = &http.Client{}
	}
	a.HTTPClient.Timeout = options.HTTPTimeout
	a.AURRetries = options.AURRetries
	a.AURRetryDelay = options.AURRetryDelay
	if options.Logger != nil {
		a.Logger = options.Logger
	}
	return nil
}

// IsInstalled reports whether a package is installed.
func (a *Arch) IsInstalled(ctx context.Context, packageName string) (bool, error) {
	if _, err := a.Runner.LookPath("pacman"); err != nil {
		return false, fmt.Errorf("pacman is required: %w", err)
	}
	_, err := a.Runner.Output(ctx, "pacman", []string{"-Q", packageName})
	if err == nil {
		return true, nil
	}
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check installed package %s: %w", packageName, err)
}

// RepositoryPackages returns packages available from system repositories.
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
	Error   string `json:"error"`
	Results []struct {
		Name string
	} `json:"results"`
}

// AURPackages returns packages available from the AUR.
func (a *Arch) AURPackages(ctx context.Context, names []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(names) == 0 {
		return result, nil
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
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
		payload, err := a.aurBatch(ctx, client, query.Encode())
		if err != nil {
			return nil, err
		}
		for _, packageInfo := range payload.Results {
			result[packageInfo.Name] = struct{}{}
		}
	}
	return result, nil
}

func (a *Arch) aurBatch(ctx context.Context, client *http.Client, query string) (aurResponse, error) {
	var lastErr error
	retries := a.AURRetries
	if retries < 1 {
		retries = 3
	}
	delay := a.AURRetryDelay
	if delay < 0 {
		delay = 100 * time.Millisecond
	}
	for attempt := 0; attempt < retries; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://aur.archlinux.org/rpc/v5/info?"+query, nil)
		if err != nil {
			return aurResponse{}, fmt.Errorf("create AUR request: %w", err)
		}
		response, err := client.Do(request)
		if err != nil {
			lastErr = fmt.Errorf("AUR request failed: %w", err)
		} else if response.Body == nil {
			lastErr = fmt.Errorf("AUR response has no body")
		} else if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
			response.Body.Close()
			lastErr = fmt.Errorf("AUR returned HTTP %s", response.Status)
		} else if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return aurResponse{}, fmt.Errorf("AUR returned HTTP %s", response.Status)
		} else {
			var payload aurResponse
			decodeError := json.NewDecoder(response.Body).Decode(&payload)
			closeError := response.Body.Close()
			if decodeError != nil {
				return aurResponse{}, fmt.Errorf("decode AUR response: %w", decodeError)
			}
			if closeError != nil {
				return aurResponse{}, fmt.Errorf("close AUR response: %w", closeError)
			}
			if payload.Error != "" {
				return aurResponse{}, fmt.Errorf("AUR error: %s", payload.Error)
			}
			return payload, nil
		}
		if attempt < retries-1 {
			backoff := delay * time.Duration(1<<attempt)
			a.logf("AUR request attempt %d/%d failed: %v; retrying in %s", attempt+1, retries, lastErr, backoff)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return aurResponse{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return aurResponse{}, lastErr
}

func (a *Arch) logf(format string, values ...any) {
	if a.Logger != nil {
		fmt.Fprintf(a.Logger, "dotpkg: "+format+"\n", values...)
	}
}

// Install installs the requested managed item.
func (a *Arch) Install(ctx context.Context, repository, aur []string, _ string) error {
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

// Remove removes the requested managed item.
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

// CurrentGroups returns the groups of the current user.
func (a *Arch) CurrentGroups(ctx context.Context, username string) ([]string, error) {
	output, err := a.Runner.Output(ctx, "id", []string{"-nG", username})
	if err != nil {
		return nil, fmt.Errorf("list groups for %s: %w", username, err)
	}
	return strings.Fields(string(output)), nil
}

// GroupExists reports whether a system group exists.
func (a *Arch) GroupExists(ctx context.Context, group string) (bool, error) {
	_, err := a.Runner.Output(ctx, "getent", []string{"group", group})
	if err == nil {
		return true, nil
	}
	if code, ok := exitCode(err); ok && code == 2 {
		return false, nil
	}
	return false, fmt.Errorf("check group %s: %w", group, err)
}

// AddToGroup adds the user to a system group.
func (a *Arch) AddToGroup(ctx context.Context, username, group string) error {
	if err := a.Runner.Run(ctx, "sudo", []string{"usermod", "-aG", group, username}, ""); err != nil {
		return fmt.Errorf("add %s to group %s: %w", username, group, err)
	}
	return nil
}

// RemoveFromGroup removes the user from a system group.
func (a *Arch) RemoveFromGroup(ctx context.Context, username, group string) error {
	if err := a.Runner.Run(ctx, "sudo", []string{"gpasswd", "-d", username, group}, ""); err != nil {
		return fmt.Errorf("remove %s from group %s: %w", username, group, err)
	}
	return nil
}

// ServiceExists reports whether a service unit exists.
func (a *Arch) ServiceExists(ctx context.Context, user bool, service string) (bool, error) {
	args := serviceArgs(user, "cat", service)
	_, err := a.Runner.Output(ctx, "systemctl", args)
	if err == nil {
		return true, nil
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check service %s: %w", service, err)
}

// ServiceEnabled reports whether a service unit is enabled.
func (a *Arch) ServiceEnabled(ctx context.Context, user bool, service string) (bool, error) {
	args := serviceArgs(user, "is-enabled", "--quiet", service)
	_, err := a.Runner.Output(ctx, "systemctl", args)
	if err == nil {
		return true, nil
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check enabled service %s: %w", service, err)
}

// EnableService enables a service unit.
func (a *Arch) EnableService(ctx context.Context, user bool, service string) error {
	if err := a.runSystemctl(ctx, user, "enable", "--now", service); err != nil {
		return fmt.Errorf("enable service %s: %w", service, err)
	}
	return nil
}

// DisableService disables a service unit.
func (a *Arch) DisableService(ctx context.Context, user bool, service string) error {
	if err := a.runSystemctl(ctx, user, "disable", "--now", service); err != nil {
		return fmt.Errorf("disable service %s: %w", service, err)
	}
	return nil
}

// ReloadServices reloads the service manager configuration.
func (a *Arch) ReloadServices(ctx context.Context, user bool) error {
	if err := a.runSystemctl(ctx, user, "daemon-reload"); err != nil {
		return fmt.Errorf("reload %s services: %w", serviceScope(user), err)
	}
	return nil
}

// RestartService restarts a service unit.
func (a *Arch) RestartService(ctx context.Context, user bool, service string) error {
	if err := a.runSystemctl(ctx, user, "try-restart", service); err != nil {
		return fmt.Errorf("restart service %s: %w", service, err)
	}
	return nil
}

// ServiceActive reports whether a service unit is active.
func (a *Arch) ServiceActive(ctx context.Context, user bool, service string) (bool, error) {
	n := nativeResources{Runner: a.Runner}
	return n.ServiceActive(ctx, user, service)
}

// ReloadService reloads a service unit.
func (a *Arch) ReloadService(ctx context.Context, user bool, service string) error {
	return a.runSystemctl(ctx, user, "reload", service)
}

func (a *Arch) runSystemctl(ctx context.Context, user bool, args ...string) error {
	args = serviceArgs(user, args...)
	if user {
		return a.Runner.Run(ctx, "systemctl", args, "")
	}
	return a.Runner.Run(ctx, "sudo", append([]string{"systemctl"}, args...), "")
}

func serviceScope(user bool) string {
	if user {
		return "user"
	}
	return "system"
}

func serviceArgs(user bool, args ...string) []string {
	if user {
		return append([]string{"--user"}, args...)
	}
	return args
}

func exitCode(err error) (int, bool) {
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), true
	}
	return 0, false
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
	if err := a.Runner.Run(ctx, "git", []string{"-C", checkout, "checkout", "--detach", yayBootstrapRevision}, ""); err != nil {
		return fmt.Errorf("pin yay source %s: %w", yayBootstrapRevision, err)
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
