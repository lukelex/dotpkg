package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Backend is the small system boundary needed by the manifest reconciler.
// Implementations may use native package-manager commands, while the planner
// and state engine remain independent of a particular distribution.
type Backend interface {
	IsInstalled(context.Context, string) (bool, error)
	RepositoryPackages(context.Context) (map[string]struct{}, error)
	AURPackages(context.Context, []string) (map[string]struct{}, error)
	Install(context.Context, []string, []string, string) error
	Remove(context.Context, []string, string) error
}

// Options controls operational behavior shared by backend implementations.
// Distribution-specific settings should remain behind Configurable rather than
// leaking into reconciliation and manifest code.
type Options struct {
	HTTPTimeout   time.Duration
	AURRetries    int
	AURRetryDelay time.Duration
	Logger        io.Writer
}

// Configurable is implemented by backends that expose operational tuning.
type Configurable interface {
	Configure(Options) error
}

// New constructs the configured package backend. DOTPKG_BACKEND is an
// explicit extension point for future distribution backends; Arch remains the
// default for compatibility with the current package format.
func New() (Backend, error) {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("DOTPKG_BACKEND")))
	if name == "" {
		name = "arch"
	}
	switch name {
	case "arch", "archlinux":
		return NewArch(), nil
	case "debian", "ubuntu":
		return NewDebian(), nil
	case "fedora", "rhel":
		return NewFedora(), nil
	default:
		return nil, fmt.Errorf("unsupported package backend %q (supported: arch, debian, fedora)", name)
	}
}
