package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lukelex/dotpkg/internal/appimage"
	"github.com/lukelex/dotpkg/internal/backend"
	githubsource "github.com/lukelex/dotpkg/internal/github"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/recovery"
	"github.com/lukelex/dotpkg/internal/resource"
	"github.com/lukelex/dotpkg/internal/state"
)

func Sync(ctx context.Context, options Options, system backend.Backend) error {
	return WithLock(options, func(normalized Options) error {
		return syncLocked(ctx, normalized, system)
	})
}

func Recover(ctx context.Context, options Options, system backend.Backend) error {
	normalized, err := options.normalize()
	if err != nil {
		return err
	}
	journal, err := recovery.Load(normalized.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(normalized.Output, "recovery: no pending package transaction")
		return nil
	}
	if err != nil {
		return err
	}
	if journal.Completed {
		return recovery.Clear(normalized.StatePath)
	}
	if !normalized.Yes {
		apply, askErr := askSelection(normalized, "Rollback the recorded package transaction? [y/N] ", false)
		if askErr != nil {
			return askErr
		}
		if !apply {
			return nil
		}
	}
	appSystem, err := appImageBackend(normalized)
	if err != nil {
		return err
	}
	githubSystem, err := githubBackend(normalized)
	if err != nil {
		return err
	}
	if err := rollback(ctx, system, appSystem, githubSystem, journal); err != nil {
		return fmt.Errorf("recovery failed; journal retained: %w", err)
	}
	return recovery.Clear(normalized.StatePath)
}

func SyncLocked(ctx context.Context, options Options, system backend.Backend) error {
	normalized, err := options.normalize()
	if err != nil {
		return err
	}
	return syncLocked(ctx, normalized, system)
}

func syncLocked(ctx context.Context, options Options, system backend.Backend) error {
	m, s, options, err := Load(options)
	if err != nil {
		return err
	}
	selectionsChanged, err := EnsureSelections(m, s, options)
	if err != nil {
		return err
	}
	if selectionsChanged && !options.DryRun {
		setCurrentState(s, m, options)
		if err := s.Write(); err != nil {
			return err
		}
	}
	plan, err := BuildPlan(ctx, m, s, options.Profile, system)
	if err != nil {
		return err
	}
	appSystem, err := appImageBackend(options)
	if err != nil {
		return err
	}
	appImagePlan, err := BuildAppImagePlan(ctx, m, s, options.Profile, appSystem)
	if err != nil {
		return err
	}
	githubSystem, err := githubBackend(options)
	if err != nil {
		return err
	}
	githubPlan, err := BuildGitHubPlan(ctx, m, s, options.Profile, githubSystem)
	if err != nil {
		return err
	}
	var resourcePlan *resource.Plan
	if options.Resources && options.OutputFormat == "json" {
		resourceSystem, resourceErr := resourceBackend(options, system)
		if resourceErr != nil {
			return resourceErr
		}
		planned, resourceErr := resource.BuildPlan(ctx, m, s, resource.Options{
			Profile:  options.Profile,
			RootPath: options.RootPath,
			Output:   options.Output,
		}, resourceSystem)
		if resourceErr != nil {
			return resourceErr
		}
		resourcePlan = &planned
	}
	printPlanWithGitHub(options.Output, plan, options.OutputFormat, resourcePlan, options, appImagePlan, githubPlan)
	if plan.Changes() == 0 && appImagePlan.Changes() == 0 && githubPlan.Changes() == 0 {
		if options.OutputFormat == "text" {
			fmt.Fprintln(options.Output, "packages, AppImages, and GitHub artifacts: already synchronized")
		}
		if !options.Resources {
			return nil
		}
	}
	if options.DryRun {
		return syncResources(ctx, m, s, options, system)
	}
	apply := options.Yes
	if !options.Yes {
		apply, err = askSelection(options, "Apply package, AppImage, and GitHub artifact changes? [y/N] ", false)
		if err != nil {
			return err
		}
	}
	if !apply {
		return syncResources(ctx, m, s, options, system)
	}
	repository, aur, err := splitByOrigin(m, plan.Missing)
	if err != nil {
		return err
	}
	oldPackages := append([]string{}, s.Packages()...)
	oldOrigins := s.PackageOrigins()
	oldState, err := s.Snapshot()
	if err != nil {
		return err
	}
	journal := recovery.New(options.StatePath, options.Profile,
		packageGroup(repository, aur, nil),
		packageGroupFromState(plan.Extra, oldOrigins, m),
	)
	for _, name := range appImagePlan.Missing {
		journal.AppImages.Installed = append(journal.AppImages.Installed, recoveryAppImageRecord(appImagePlan.Records[name]))
		if previous, ok := appImagePlan.Previous[name]; ok {
			journal.AppImages.Removed = append(journal.AppImages.Removed, recoveryAppImageRecord(previous))
		}
	}
	for _, name := range appImagePlan.Extra {
		journal.AppImages.Removed = append(journal.AppImages.Removed, recoveryAppImageRecord(appImagePlan.Previous[name]))
	}
	for _, name := range githubPlan.Extra {
		journal.GitHub.Removed = append(journal.GitHub.Removed, recoveryGitHubRecord(githubPlan.Previous[name]))
	}
	packageChanges := len(plan.Missing) + len(plan.Extra)
	appImageChanges := appImagePlan.Changes()
	githubChanges := githubPlan.Changes()
	if packageChanges > 0 || appImageChanges > 0 || githubChanges > 0 {
		if err := journal.Write(); err != nil {
			return err
		}
	}
	if err := system.Install(ctx, repository, aur, options.Profile); err != nil {
		return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
	}
	if err := system.Remove(ctx, plan.Extra, options.Profile); err != nil {
		return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
	}
	for _, name := range appImagePlan.Missing {
		if _, err := appSystem.Install(ctx, appImagePlan.Specs[name], appImagePlan.Artifacts[name]); err != nil {
			return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
		if previous, ok := appImagePlan.Previous[name]; ok && previous.Target != appImagePlan.Records[name].Target {
			if err := appSystem.Remove(ctx, previous); err != nil {
				return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
			}
		}
	}
	for _, name := range appImagePlan.Extra {
		if err := appSystem.Remove(ctx, appImagePlan.Previous[name]); err != nil {
			return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
	}
	for _, name := range githubPlan.Missing {
		var previous *githubsource.Record
		if record, ok := githubPlan.Previous[name]; ok {
			copy := record
			previous = &copy
			journal.GitHub.Removed = append(journal.GitHub.Removed, recoveryGitHubRecord(record))
		}
		record, installErr := githubSystem.Install(ctx, githubPlan.Specs[name], githubPlan.Artifacts[name], previous)
		if installErr != nil {
			return packageFailure(installErr, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
		githubPlan.Records[name] = record
		journal.GitHub.Installed = append(journal.GitHub.Installed, recoveryGitHubRecord(record))
		if err := journal.Write(); err != nil {
			return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
		if previous != nil {
			stale := staleGitHubFiles(*previous, record)
			if len(stale.Files) > 0 {
				if err := githubSystem.Remove(ctx, stale); err != nil {
					return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
				}
			}
		}
	}
	for _, name := range githubPlan.Extra {
		if err := githubSystem.Remove(ctx, githubPlan.Previous[name]); err != nil {
			return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
	}
	tracked := append([]string{}, s.Packages()...)
	tracked = subtract(tracked, plan.Extra)
	tracked = append(tracked, plan.Adopted...)
	tracked = append(tracked, plan.Missing...)
	s.SetPackages(tracked)
	origins := s.PackageOrigins()
	for _, packageName := range plan.Extra {
		delete(origins, packageName)
	}
	for _, packageName := range tracked {
		if origin := m.PackageOrigin(packageName); origin == "repo" || origin == "aur" {
			origins[packageName] = origin
		}
	}
	s.SetPackageOrigins(origins)
	images := s.AppImages()
	for _, name := range appImagePlan.Extra {
		delete(images, name)
	}
	for _, name := range append(append([]string{}, appImagePlan.Adopted...), appImagePlan.Missing...) {
		images[name] = state.AppImage{
			Address:   appImagePlan.Records[name].Address,
			Target:    appImagePlan.Records[name].Target,
			Algorithm: appImagePlan.Records[name].Algorithm,
			Digest:    appImagePlan.Records[name].Digest,
			Version:   appImagePlan.Records[name].Version,
		}
	}
	s.SetAppImages(images)
	artifacts := s.GitHubArtifacts()
	for _, name := range githubPlan.Extra {
		delete(artifacts, name)
	}
	for _, name := range append(append([]string{}, githubPlan.Adopted...), githubPlan.Missing...) {
		artifacts[name] = stateGitHubArtifact(githubPlan.Records[name])
	}
	s.SetGitHubArtifacts(artifacts)
	setCurrentState(s, m, options)
	if err := s.Write(); err != nil {
		return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
	}
	if err := syncResources(ctx, m, s, options, system); err != nil {
		if packageChanges > 0 || appImageChanges > 0 || githubChanges > 0 {
			return packageFailure(err, ctx, system, appSystem, githubSystem, journal, s, oldState, oldPackages, oldOrigins)
		}
		return err
	}
	if packageChanges > 0 || appImageChanges > 0 || githubChanges > 0 {
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

func packageGroup(repository, aur, unknown []string) recovery.Packages {
	return recovery.Packages{Repository: append([]string{}, repository...), AUR: append([]string{}, aur...), Unknown: append([]string{}, unknown...)}
}

func stateAppImageRecord(name string, image state.AppImage) appimage.Record {
	return appimage.Record{
		Name:      name,
		Address:   image.Address,
		Target:    image.Target,
		Algorithm: image.Algorithm,
		Digest:    image.Digest,
		Version:   image.Version,
	}
}

func recoveryAppImageRecord(record appimage.Record) recovery.AppImage {
	return recovery.AppImage{
		Name:      record.Name,
		Address:   record.Address,
		Target:    record.Target,
		Algorithm: record.Algorithm,
		Digest:    record.Digest,
		Version:   record.Version,
	}
}

func appImageRecordFromRecovery(record recovery.AppImage) appimage.Record {
	return appimage.Record{
		Name:      record.Name,
		Address:   record.Address,
		Target:    record.Target,
		Algorithm: record.Algorithm,
		Digest:    record.Digest,
		Version:   record.Version,
	}
}

func recoveryGitHubRecord(record githubsource.Record) recovery.GitHubArtifact {
	result := recovery.GitHubArtifact{Name: record.Name, Repo: record.Repo, Ref: record.Ref, Archive: record.Archive, Sha256: record.Sha256, Files: make([]recovery.GitHubFile, 0, len(record.Files))}
	for _, file := range record.Files {
		result.Files = append(result.Files, recovery.GitHubFile{Source: file.Source, Target: file.Target, Digest: file.Digest})
	}
	return result
}

func githubRecordFromRecovery(record recovery.GitHubArtifact) githubsource.Record {
	result := githubsource.Record{Name: record.Name, Repo: record.Repo, Ref: record.Ref, Archive: record.Archive, Sha256: record.Sha256, Files: make([]githubsource.InstalledFile, 0, len(record.Files))}
	for _, file := range record.Files {
		result.Files = append(result.Files, githubsource.InstalledFile{Source: file.Source, Target: file.Target, Digest: file.Digest})
	}
	return result
}

func packageGroupFromState(packages []string, origins map[string]string, m *manifest.Manifest) recovery.Packages {
	var repository, aur, unknown []string
	for _, packageName := range packages {
		origin := origins[packageName]
		if origin == "" {
			origin = m.PackageOrigin(packageName)
		}
		switch origin {
		case "repo":
			repository = append(repository, packageName)
		case "aur":
			aur = append(aur, packageName)
		default:
			unknown = append(unknown, packageName)
		}
	}
	return packageGroup(repository, aur, unknown)
}

func packageFailure(original error, ctx context.Context, system backend.Backend, appSystem appimage.System, githubSystem githubsource.System, journal recovery.Journal, s *state.State, oldState map[string]any, oldPackages []string, oldOrigins map[string]string) error {
	rollbackErr := rollback(ctx, system, appSystem, githubSystem, journal)
	s.Restore(oldState)
	s.SetPackages(oldPackages)
	s.SetPackageOrigins(oldOrigins)
	stateErr := s.Write()
	if rollbackErr != nil || stateErr != nil {
		return fmt.Errorf("%w; recovery required (rollback=%v, state=%v)", original, rollbackErr, stateErr)
	}
	if err := recovery.Clear(journal.StatePath); err != nil {
		return fmt.Errorf("%w; remove recovery journal: %v", original, err)
	}
	return original
}

func rollback(ctx context.Context, system backend.Backend, appSystem appimage.System, githubSystem githubsource.System, journal recovery.Journal) error {
	var errorsFound []string
	var installed []string
	for _, packageName := range append(append([]string{}, journal.Installed.Repository...), journal.Installed.AUR...) {
		present, err := system.IsInstalled(ctx, packageName)
		if err != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("check installed %s: %v", packageName, err))
		} else if present {
			installed = append(installed, packageName)
		}
	}
	installed = append(installed, journal.Installed.Unknown...)
	if len(installed) > 0 {
		if err := system.Remove(ctx, installed, journal.Profile); err != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("remove newly installed packages: %v", err))
		}
	}
	if len(journal.Removed.Unknown) > 0 {
		errorsFound = append(errorsFound, "cannot restore packages with unknown origins: "+strings.Join(journal.Removed.Unknown, ", "))
	}
	if len(journal.Removed.Repository) > 0 || len(journal.Removed.AUR) > 0 {
		if err := system.Install(ctx, journal.Removed.Repository, journal.Removed.AUR, journal.Profile); err != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("restore removed packages: %v", err))
		}
	}
	if appSystem != nil {
		for _, record := range journal.AppImages.Installed {
			appRecord := appImageRecordFromRecovery(record)
			present, err := appSystem.Installed(ctx, appRecord)
			if err != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("check newly installed AppImage %s: %v", record.Name, err))
			} else if present {
				err = appSystem.Remove(ctx, appRecord)
			}
			if err != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("remove newly installed AppImage %s: %v", record.Name, err))
			}
		}
		for _, record := range journal.AppImages.Removed {
			appRecord := appImageRecordFromRecovery(record)
			spec := appimage.Spec{Name: appRecord.Name, Address: appRecord.Address, Target: appRecord.Target}
			artifact := appimage.Artifact{Address: appRecord.Address, Algorithm: appRecord.Algorithm, Digest: appRecord.Digest, Version: appRecord.Version}
			if _, err := appSystem.Install(ctx, spec, artifact); err != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("restore removed AppImage %s: %v", record.Name, err))
			}
		}
	}
	if githubSystem != nil {
		for _, record := range journal.GitHub.Installed {
			githubRecord := githubRecordFromRecovery(record)
			present, err := githubSystem.Installed(ctx, githubRecord)
			if err == nil && present {
				err = githubSystem.Remove(ctx, githubRecord)
			}
			if err != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("remove newly installed GitHub artifact %s: %v", record.Name, err))
			}
		}
		for _, record := range journal.GitHub.Removed {
			githubRecord := githubRecordFromRecovery(record)
			spec := githubsource.Spec{Name: githubRecord.Name, Repo: githubRecord.Repo, Ref: githubRecord.Ref, Archive: githubRecord.Archive, Sha256: githubRecord.Sha256}
			for _, file := range githubRecord.Files {
				spec.Files = append(spec.Files, githubsource.File{Source: file.Source, Target: file.Target})
			}
			artifact, resolveErr := githubSystem.Resolve(ctx, spec)
			if resolveErr == nil {
				_, resolveErr = githubSystem.Install(ctx, spec, artifact, &githubRecord)
			}
			if resolveErr != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("restore GitHub artifact %s: %v", record.Name, resolveErr))
			}
		}
	}
	if len(errorsFound) > 0 {
		return errors.New(strings.Join(errorsFound, "; "))
	}
	return nil
}

func appImageBackend(options Options) (appimage.System, error) {
	if options.AppImageSystem != nil {
		return options.AppImageSystem, nil
	}
	return appimage.New(), nil
}

func githubBackend(options Options) (githubsource.System, error) {
	if options.GitHubSystem != nil {
		return options.GitHubSystem, nil
	}
	return githubsource.New(), nil
}

func staleGitHubFiles(previous, current githubsource.Record) githubsource.Record {
	currentTargets := make(map[string]struct{}, len(current.Files))
	for _, file := range current.Files {
		currentTargets[file.Target] = struct{}{}
	}
	stale := previous
	stale.Files = stale.Files[:0]
	for _, file := range previous.Files {
		if _, retained := currentTargets[file.Target]; !retained {
			stale.Files = append(stale.Files, file)
		}
	}
	return stale
}

func syncResources(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system backend.Backend) error {
	if !options.Resources {
		return nil
	}
	resourceSystem, err := resourceBackend(options, system)
	if err != nil {
		return err
	}
	if !options.DryRun {
		setCurrentState(s, m, options)
		if err := s.Write(); err != nil {
			return err
		}
	}
	return resource.Sync(ctx, m, s, resource.Options{
		Profile:         options.Profile,
		RootPath:        options.RootPath,
		DryRun:          options.DryRun,
		Yes:             options.Yes,
		Replace:         options.Replace,
		RestartServices: options.RestartServices,
		Input:           options.Input,
		Output:          options.Output,
		OutputFormat:    options.OutputFormat,
		SuppressPlan:    options.OutputFormat == "json",
	}, resourceSystem)
}

func resourceBackend(options Options, system backend.Backend) (resource.System, error) {
	if options.ResourceSystem != nil {
		return options.ResourceSystem, nil
	}
	resourceSystem, ok := system.(resource.System)
	if !ok {
		return nil, fmt.Errorf("backend does not support resource reconciliation")
	}
	return resourceSystem, nil
}
