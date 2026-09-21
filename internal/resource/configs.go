package resource

import (
	"errors"
	"fmt"
	"os"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

func planConfigs(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
	declared := declaredMetadata(m, s, options.Profile, "configs")
	planned := StagePlan{Declared: declared}
	tracked := s.Items("managed", "configs")
	trackedSet := stringSet(tracked)
	for _, mapping := range declared {
		source, target, err := configPaths(mapping, options)
		if err != nil {
			return StagePlan{}, err
		}
		if _, err := os.Stat(source); err != nil {
			return StagePlan{}, fmt.Errorf("config source %s: %w", source, err)
		}
		matches, err := configMatches(target, source)
		if err != nil {
			return StagePlan{}, err
		}
		if matches {
			if !s.Exists {
				if _, tracked := trackedSet[mapping]; !tracked {
					planned.Adopted = append(planned.Adopted, mapping)
				}
			}
		} else {
			planned.Missing = append(planned.Missing, mapping)
		}
	}
	planned.Extra = subtract(tracked, declared)
	return sortPlan(planned), nil
}

func applyConfigs(plan StagePlan, options Options) error {
	for _, mapping := range plan.Missing {
		source, target, err := configPaths(mapping, options)
		if err != nil {
			return err
		}
		if err := linkConfig(source, target, options.Replace); err != nil {
			if errors.Is(err, errConfigConflict) {
				fmt.Fprintf(options.Output, "skipping %s: exists and is not a repo link; use --replace to overwrite\n", target)
				continue
			}
			return err
		}
	}
	for _, mapping := range plan.Extra {
		source, target, err := configPaths(mapping, options)
		if err != nil {
			return err
		}
		matches, err := configMatches(target, source)
		if err != nil {
			return err
		}
		if matches {
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("remove config link %s: %w", target, err)
			}
		}
	}
	return nil
}
