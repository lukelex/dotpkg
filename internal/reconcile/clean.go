package reconcile

import (
	"context"
	"fmt"

	"github.com/lukelex/dotpkg/internal/appimage"
	"github.com/lukelex/dotpkg/internal/backend"
	githubsource "github.com/lukelex/dotpkg/internal/github"
	"github.com/lukelex/dotpkg/internal/recovery"
	"github.com/lukelex/dotpkg/internal/resource"
)

func cleanLocked(ctx context.Context, options Options, system backend.Backend) error {
	m, s, options, err := Load(options)
	if err != nil {
		return err
	}
	declared := packageManagerPackages(m, options.Profile, s)
	packagePlan := Plan{Declared: declared, Extra: subtract(s.Packages(), declared)}
	appImagePlan := BuildAppImageCleanPlan(m, s, options.Profile)
	githubPlan := BuildGitHubCleanPlan(m, s, options.Profile)
	var resourcePlan *resource.Plan
	if options.Resources {
		resourceOptions := resource.Options{Profile: options.Profile, RootPath: options.RootPath, Output: options.Output}
		planned, resourceErr := resource.CleanPlan(m, s, resourceOptions)
		if resourceErr != nil {
			return resourceErr
		}
		resourcePlan = &planned
	}
	printPlanWithGitHub(options.Output, packagePlan, options.OutputFormat, resourcePlan, options, appImagePlan, githubPlan)
	changes := len(packagePlan.Extra) + len(appImagePlan.Extra) + len(githubPlan.Extra)
	if resourcePlan != nil {
		changes += resourcePlan.Changes()
	}
	if changes == 0 || options.DryRun {
		return nil
	}
	if !options.Yes {
		apply, askErr := askSelection(options, "Remove managed items no longer declared? [y/N] ", false)
		if askErr != nil {
			return askErr
		}
		if !apply {
			return nil
		}
	}
	oldPackages := append([]string{}, s.Packages()...)
	oldOrigins := s.PackageOrigins()
	oldState, err := s.Snapshot()
	if err != nil {
		return err
	}
	journal := recovery.New(options.StatePath, options.Profile, recovery.Packages{}, packageGroupFromState(packagePlan.Extra, oldOrigins, m))
	for _, name := range appImagePlan.Extra {
		journal.AppImages.Removed = append(journal.AppImages.Removed, recoveryAppImageRecord(appImagePlan.Previous[name]))
	}
	for _, name := range githubPlan.Extra {
		journal.GitHub.Removed = append(journal.GitHub.Removed, recoveryGitHubRecord(githubPlan.Previous[name]))
	}
	var appSystem appimage.System
	var githubSystem githubsource.System
	if len(appImagePlan.Extra) > 0 {
		appSystem, err = appImageBackend(options)
		if err != nil {
			return err
		}
	}
	if len(githubPlan.Extra) > 0 {
		githubSystem, err = githubBackend(options)
		if err != nil {
			return err
		}
	}
	if len(packagePlan.Extra) > 0 || len(appImagePlan.Extra) > 0 || len(githubPlan.Extra) > 0 {
		if err := journal.Write(); err != nil {
			return err
		}
	}
	if len(packagePlan.Extra) > 0 {
		if err := system.Remove(ctx, packagePlan.Extra, options.Profile); err != nil {
			return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
	}
	if len(appImagePlan.Extra) > 0 {
		for _, name := range appImagePlan.Extra {
			if err := appSystem.Remove(ctx, appImagePlan.Previous[name]); err != nil {
				return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
			}
		}
	}
	if len(githubPlan.Extra) > 0 {
		for _, name := range githubPlan.Extra {
			if err := githubSystem.Remove(ctx, githubPlan.Previous[name]); err != nil {
				return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
			}
		}
	}
	if len(packagePlan.Extra) > 0 {
		s.SetPackages(subtract(s.Packages(), packagePlan.Extra))
		origins := s.PackageOrigins()
		for _, packageName := range packagePlan.Extra {
			delete(origins, packageName)
		}
		s.SetPackageOrigins(origins)
		setCurrentState(s, m, options)
	}
	if len(appImagePlan.Extra) > 0 {
		images := s.AppImages()
		for _, name := range appImagePlan.Extra {
			delete(images, name)
		}
		s.SetAppImages(images)
	}
	if len(githubPlan.Extra) > 0 {
		artifacts := s.GitHubArtifacts()
		for _, name := range githubPlan.Extra {
			delete(artifacts, name)
		}
		s.SetGitHubArtifacts(artifacts)
	}
	if len(packagePlan.Extra) > 0 || len(appImagePlan.Extra) > 0 || len(githubPlan.Extra) > 0 {
		setCurrentState(s, m, options)
		if err := s.Write(); err != nil {
			return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
	}
	if resourcePlan != nil && resourcePlan.Changes() > 0 {
		resourceSystem, backendErr := resourceBackend(options, system)
		if backendErr != nil {
			return backendErr
		}
		setCurrentState(s, m, options)
		if err := resource.Clean(ctx, m, s, resource.Options{Profile: options.Profile, RootPath: options.RootPath, Output: options.Output}, resourceSystem); err != nil {
			if len(packagePlan.Extra) > 0 || len(appImagePlan.Extra) > 0 || len(githubPlan.Extra) > 0 {
				return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
			}
			return err
		}
	}
	if len(packagePlan.Extra) > 0 || len(appImagePlan.Extra) > 0 || len(githubPlan.Extra) > 0 {
		journal.Completed = true
		if err := journal.Write(); err != nil {
			return fmt.Errorf("complete recovery journal: %w", err)
		}
		if err := recovery.Clear(options.StatePath); err != nil {
			return err
		}
	}
	return nil
}
