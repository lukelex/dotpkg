package resource

import (
	"context"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

func planGroups(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System) (StagePlan, error) {
	declared := declaredMetadata(m, s, options.Profile, "groups")
	current, err := system.CurrentGroups(ctx, options.User)
	if err != nil {
		return StagePlan{}, err
	}
	currentSet := stringSet(current)
	planned := StagePlan{Declared: declared}
	for _, group := range declared {
		exists, err := system.GroupExists(ctx, group)
		if err != nil {
			return StagePlan{}, err
		}
		if !exists {
			continue
		}
		if _, present := currentSet[group]; present {
			if !contains(s.Items("managed", "groups"), group) {
				planned.Adopted = append(planned.Adopted, group)
			}
		} else {
			planned.Missing = append(planned.Missing, group)
		}
	}
	planned.Extra = subtract(s.Items("managed", "groups"), declared)
	return sortPlan(planned), nil
}

func applyGroups(ctx context.Context, plan StagePlan, options Options, system System) error {
	for _, group := range plan.Missing {
		if err := system.AddToGroup(ctx, options.User, group); err != nil {
			return err
		}
	}
	for _, group := range plan.Extra {
		if err := system.RemoveFromGroup(ctx, options.User, group); err != nil {
			return err
		}
	}
	return nil
}

func cleanGroups(ctx context.Context, groups []string, options Options, system System) error {
	if len(groups) == 0 {
		return nil
	}
	currentGroups, err := system.CurrentGroups(ctx, options.User)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if !contains(currentGroups, group) {
			continue
		}
		if err := system.RemoveFromGroup(ctx, options.User, group); err != nil {
			return err
		}
	}
	return nil
}
