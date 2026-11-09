package resource

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

func planServices(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System, allowMissingConfigLinks bool) (StagePlan, error) {
	declared := declaredServices(m, s, options.Profile)
	configs := declaredMetadata(m, s, options.Profile, "configs")
	planned := StagePlan{Declared: declared}
	trackedSet := stringSet(s.Items(state.ManagedKey, state.ManagedServices))
	for _, service := range declared {
		userService, name := strings.HasPrefix(service, "user:"), strings.TrimPrefix(service, "user:")
		exists, err := system.ServiceExists(ctx, userService, name)
		if err != nil {
			return StagePlan{}, err
		}
		if !exists && configProvidesService(configs, service, options, allowMissingConfigLinks) {
			exists = true
		}
		if !exists {
			continue
		}
		enabled, err := system.ServiceEnabled(ctx, userService, name)
		if err != nil {
			return StagePlan{}, err
		}
		if enabled {
			if _, tracked := trackedSet[service]; !tracked {
				planned.Adopted = append(planned.Adopted, service)
			}
		} else {
			planned.Missing = append(planned.Missing, service)
		}
	}
	planned.Extra = subtract(s.Items(state.ManagedKey, state.ManagedServices), declared)
	return sortPlan(planned), nil
}

func configProvidesService(configs []string, service string, options Options, allowMissingConfigLinks bool) bool {
	for _, mapping := range configs {
		source, target, err := configPaths(mapping, options)
		if err != nil || !strings.HasSuffix(filepath.ToSlash(target), servicePath(service)) {
			continue
		}
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if allowMissingConfigLinks {
			return true
		}
		matches, err := configMatches(target, source)
		if err == nil && matches {
			return true
		}
	}
	return false
}

func configMatchesService(configs []string, service string, options Options) bool {
	for _, mapping := range configs {
		source, target, err := configPaths(mapping, options)
		if err != nil || !strings.HasSuffix(filepath.ToSlash(target), servicePath(service)) {
			continue
		}
		matches, err := configMatches(target, source)
		if err == nil && matches {
			return true
		}
	}
	return false
}

func servicePath(service string) string {
	name := strings.TrimPrefix(service, "user:")
	if strings.HasPrefix(service, "user:") {
		return "/systemd/user/" + name
	}
	return "/systemd/system/" + name
}

func applyServices(ctx context.Context, plan StagePlan, options Options, system System) error {
	for _, service := range plan.Missing {
		userService, name := strings.HasPrefix(service, "user:"), strings.TrimPrefix(service, "user:")
		if err := system.EnableService(ctx, userService, name); err != nil {
			return err
		}
	}
	for _, service := range plan.Extra {
		userService, name := strings.HasPrefix(service, "user:"), strings.TrimPrefix(service, "user:")
		if err := system.DisableService(ctx, userService, name); err != nil {
			return err
		}
	}
	return nil
}

func cleanServices(ctx context.Context, services []string, system System) error {
	for _, service := range services {
		userService := strings.HasPrefix(service, "user:")
		name := strings.TrimPrefix(service, "user:")
		exists, err := system.ServiceExists(ctx, userService, name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := system.DisableService(ctx, userService, name); err != nil {
			return err
		}
	}
	return nil
}
