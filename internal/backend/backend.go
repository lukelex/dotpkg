package backend

import "context"

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
