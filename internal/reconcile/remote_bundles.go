package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lukelex/dotpkg/internal/remotebundle"
	"github.com/lukelex/dotpkg/internal/state"
)

func remoteBundleBackend(options Options) *remotebundle.System {
	if options.RemoteSystem != nil {
		return options.RemoteSystem
	}
	return remotebundle.New()
}

func applyRemoteBundles(ctx context.Context, s *state.State, options Options, plan *remotebundle.Plan) error {
	if plan == nil || options.DryRun {
		return nil
	}
	remoteSystem := remoteBundleBackend(options)
	for _, change := range plan.Changes {
		if !options.Yes {
			prompt := "Install remote skill bundle " + change.Name + "? [y/N] "
			if change.Action == "remove" {
				prompt = "Remove stale remote skill bundle " + change.Name + "? [y/N] "
			}
			apply, err := askSelection(options, prompt, false)
			if err != nil {
				return err
			}
			if !apply {
				continue
			}
		}
		if change.Action == "remove" {
			if err := remotebundle.Remove(change.Name, plan.Previous[change.Name]); err != nil {
				return err
			}
			bundles := s.RemoteBundles()
			delete(bundles, change.Name)
			s.SetRemoteBundles(bundles)
			if err := s.Write(); err != nil {
				return err
			}
			continue
		}
		spec := plan.Specs[change.Name]
		previous := plan.Previous[change.Name]
		if err := remoteSystem.Install(ctx, spec, previous, func() error {
			bundles := s.RemoteBundles()
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			bundles[change.Name] = state.RemoteBundle{Repository: spec.Repository, Target: filepath.Join(home, strings.TrimPrefix(spec.Target, "$HOME/")), Commit: spec.Commit, Paths: spec.Paths}
			s.SetRemoteBundles(bundles)
			return s.Write()
		}); err != nil {
			return fmt.Errorf("install remote bundle %s: %w", change.Name, err)
		}
	}
	return nil
}
