package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ServiceFiles includes effective systemd inputs and conventional override
// locations, including absent paths so newly created overrides are detected.
func (n *nativeResources) ServiceFiles(ctx context.Context, user bool, name string) ([]string, error) {
	if !strings.Contains(name, ".") {
		name += ".service"
	}
	output, err := n.Runner.Output(ctx, "systemctl", serviceArgs(user, "show", "--property=FragmentPath,DropInPaths", name))
	if err != nil {
		if code, ok := exitCode(err); !ok || (code != 1 && code != 4) {
			return nil, fmt.Errorf("inspect service inputs %s: %w", name, err)
		}
	}
	var paths []string
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "FragmentPath" && value != "" {
			paths = append(paths, value)
		}
		if key == "DropInPaths" {
			paths = append(paths, strings.Fields(value)...)
		}
	}
	roots := []string{"/etc/systemd/system", "/run/systemd/system", "/usr/local/lib/systemd/system", "/usr/lib/systemd/system"}
	if user {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local/share")
		}
		roots = []string{filepath.Join(config, "systemd/user"), filepath.Join(data, "systemd/user"), "/etc/systemd/user", "/run/systemd/user", "/usr/local/lib/systemd/user", "/usr/lib/systemd/user"}
		if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" {
			roots = append(roots, filepath.Join(runtime, "systemd/user"))
		}
	}
	for _, root := range roots {
		paths = append(paths, filepath.Join(root, name), filepath.Join(root, name+".d"))
	}
	return paths, nil
}

// ServiceFiles returns service units installed by a package.
func (a *Arch) ServiceFiles(ctx context.Context, user bool, name string) ([]string, error) {
	n := nativeResources{Runner: a.Runner}
	return n.ServiceFiles(ctx, user, name)
}
