package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/lukelex/dotpkg/internal/appimage"
	"github.com/lukelex/dotpkg/internal/backend"
	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/reconcile"
	"github.com/lukelex/dotpkg/internal/resource"
	"github.com/lukelex/dotpkg/internal/state"
)

type Finding struct {
	Severity string `json:"severity"`
	Check    string `json:"check"`
	Message  string `json:"message"`
}

type Report struct {
	Findings []Finding `json:"findings"`
}

func (r Report) Errors() int {
	count := 0
	for _, finding := range r.Findings {
		if finding.Severity == "error" {
			count++
		}
	}
	return count
}

func Run(ctx context.Context, m *manifest.Manifest, s *state.State, options reconcile.Options, system backend.Backend) Report {
	report := Report{}
	if !s.Exists {
		report.add("info", "state", "state file does not exist yet; sync will create it")
	}

	plan, err := reconcile.BuildPlan(ctx, m, s, options.Profile, system)
	if err != nil {
		report.add("error", "packages", err.Error())
	} else {
		for _, packageName := range plan.Missing {
			report.add("error", "packages", fmt.Sprintf("declared package is not installed: %s", packageName))
		}
		for _, packageName := range plan.Extra {
			report.add("warning", "packages", fmt.Sprintf("managed package is no longer declared: %s", packageName))
		}
	}
	appSystem := options.AppImageSystem
	if appSystem == nil {
		appSystem = appimage.New()
	}
	appImagePlan, err := reconcile.BuildAppImagePlan(ctx, m, s, options.Profile, appSystem)
	if err != nil {
		report.add("error", "appimages", err.Error())
	} else {
		for _, name := range appImagePlan.Missing {
			report.add("error", "appimages", fmt.Sprintf("declared AppImage is missing or stale: %s", name))
		}
		for _, name := range appImagePlan.Extra {
			report.add("warning", "appimages", fmt.Sprintf("managed AppImage is no longer declared: %s", name))
		}
	}

	validation, err := reconcile.Validate(ctx, m, options.Profile, nil, system)
	if err != nil {
		report.add("error", "backend", err.Error())
	} else {
		for _, packageName := range validation.Missing {
			report.add("error", "availability", fmt.Sprintf("package is unavailable from configured repositories: %s", packageName))
		}
	}

	resourceSystem, ok := system.(resource.System)
	if !ok {
		report.add("warning", "resources", "backend does not provide group, config, or service diagnostics")
		return report.sorted()
	}
	resourceOptions := resource.Options{
		Profile:  options.Profile,
		RootPath: options.RootPath,
		Output:   options.Output,
	}
	if resourceOptions.RootPath == "" {
		resourceOptions.RootPath = filepath.Dir(filepath.Dir(m.Path))
	}
	resourcePlan, err := resource.BuildPlan(ctx, m, s, resourceOptions, resourceSystem)
	if err != nil {
		report.add("error", "resources", err.Error())
		return report.sorted()
	}
	for _, group := range resourcePlan.Groups.Missing {
		report.add("error", "groups", fmt.Sprintf("user is not in declared group: %s", group))
	}
	for _, group := range resourcePlan.Groups.Extra {
		report.add("warning", "groups", fmt.Sprintf("managed group is no longer declared: %s", group))
	}
	for _, mapping := range resourcePlan.Configs.Declared {
		status, inspectErr := resource.InspectConfig(mapping, resourceOptions)
		if inspectErr != nil {
			report.add("error", "configs", inspectErr.Error())
			continue
		}
		if !status.SourceExists {
			report.add("error", "configs", fmt.Sprintf("config source is missing: %s", status.Source))
			continue
		}
		if !status.TargetExists {
			report.add("error", "configs", fmt.Sprintf("config link is missing: %s", status.Target))
		} else if status.TargetIsDir {
			report.add("error", "configs", fmt.Sprintf("config target is a directory: %s", status.Target))
		} else if !status.Matches {
			report.add("error", "configs", fmt.Sprintf("config target conflicts with the manifest link: %s", status.Target))
		}
	}
	for _, mapping := range resourcePlan.Configs.Extra {
		report.add("warning", "configs", fmt.Sprintf("managed config is no longer declared: %s", mapping))
	}
	for _, service := range resourcePlan.Services.Missing {
		report.add("error", "services", fmt.Sprintf("service is unavailable or disabled: %s", service))
	}
	for _, service := range resourcePlan.Services.Extra {
		report.add("warning", "services", fmt.Sprintf("managed service is no longer declared: %s", service))
	}
	return report.sorted()
}

func (r *Report) add(severity, check, message string) {
	r.Findings = append(r.Findings, Finding{Severity: severity, Check: check, Message: message})
}

func (r Report) sorted() Report {
	sort.SliceStable(r.Findings, func(i, j int) bool {
		if r.Findings[i].Severity != r.Findings[j].Severity {
			return severityRank(r.Findings[i].Severity) < severityRank(r.Findings[j].Severity)
		}
		if r.Findings[i].Check != r.Findings[j].Check {
			return r.Findings[i].Check < r.Findings[j].Check
		}
		return r.Findings[i].Message < r.Findings[j].Message
	})
	return r
}

func severityRank(value string) int {
	switch value {
	case "error":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}
