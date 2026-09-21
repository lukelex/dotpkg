package resource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type userPathRoots struct {
	home       string
	configHome string
}

func resolveUserPath(value string, allowTilde bool) (string, userPathRoots, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", userPathRoots{}, fmt.Errorf("find home directory: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	path := strings.ReplaceAll(value, "$HOME", home)
	path = strings.ReplaceAll(path, "$XDG_CONFIG_HOME", configHome)
	if allowTilde {
		switch {
		case path == "~":
			path = home
		case strings.HasPrefix(path, "~/"):
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return filepath.Clean(path), userPathRoots{home: home, configHome: configHome}, nil
}

func (roots userPathRoots) contains(path string) bool {
	return pathWithin(roots.home, path) || pathWithin(roots.configHome, path)
}
