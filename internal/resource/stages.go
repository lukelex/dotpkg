package resource

import (
	"context"
	"fmt"

	"github.com/lukelex/dotpkg/internal/manifest"
	"github.com/lukelex/dotpkg/internal/state"
)

// resourceStage defines the shared lifecycle wiring for a resource type. The
// resource-specific filesystem and system behavior remains in focused modules.
type resourceStage struct {
	name         string
	label        string
	stateKey     string
	includeEmpty bool
	plan         func(*Plan) *StagePlan
	build        func(context.Context, *manifest.Manifest, *state.State, Options, System) (StagePlan, error)
	cleanPlan    func(*manifest.Manifest, *state.State, Options) (StagePlan, error)
	apply        func(context.Context, StagePlan, *manifest.Manifest, *state.State, Options, System) error
	clean        func(context.Context, StagePlan, *manifest.Manifest, *state.State, Options, System) error
}

// NamedStage is the rendered representation of one resource lifecycle stage.
// It lets callers consume the registry without depending on Plan fields.
type NamedStage struct {
	Name         string
	Label        string
	IncludeEmpty bool
	Plan         StagePlan
}

func resourceStages() []resourceStage {
	return []resourceStage{
		{
			name: "groups", label: "GROUPS", stateKey: state.ManagedGroups, includeEmpty: true,
			plan: func(plan *Plan) *StagePlan { return &plan.Groups },
			build: func(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System) (StagePlan, error) {
				return planGroups(ctx, m, s, options, system)
			},
			cleanPlan: func(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
				declared := declaredMetadata(m, s, options.Profile, "groups")
				return StagePlan{Declared: declared, Extra: subtract(s.Items(state.ManagedKey, state.ManagedGroups), declared)}, nil
			},
			apply: func(ctx context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, options Options, system System) error {
				return applyGroups(ctx, plan, options, system)
			},
			clean: func(ctx context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, options Options, system System) error {
				return cleanGroups(ctx, plan.Extra, options, system)
			},
		},
		{
			name: "configs", label: "CONFIGS", stateKey: state.ManagedConfigs, includeEmpty: true,
			plan: func(plan *Plan) *StagePlan { return &plan.Configs },
			build: func(_ context.Context, m *manifest.Manifest, s *state.State, options Options, _ System) (StagePlan, error) {
				return planConfigs(m, s, options)
			},
			cleanPlan: func(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
				declared := declaredMetadata(m, s, options.Profile, "configs")
				return StagePlan{Declared: declared, Extra: subtract(s.Items(state.ManagedKey, state.ManagedConfigs), declared)}, nil
			},
			apply: func(_ context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, options Options, _ System) error {
				return applyConfigs(plan, options)
			},
			clean: func(ctx context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, options Options, system System) error {
				if err := applyConfigs(StagePlan{Extra: plan.Extra}, options); err != nil {
					return err
				}
				return reloadForDeclaredConfigs(ctx, plan.Extra, options, system)
			},
		},
		{
			name: "services", label: "SERVICES", stateKey: state.ManagedServices, includeEmpty: true,
			plan: func(plan *Plan) *StagePlan { return &plan.Services },
			build: func(ctx context.Context, m *manifest.Manifest, s *state.State, options Options, system System) (StagePlan, error) {
				return planServices(ctx, m, s, options, system, true)
			},
			cleanPlan: func(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
				declared := declaredServices(m, s, options.Profile)
				return StagePlan{Declared: declared, Extra: subtract(s.Items(state.ManagedKey, state.ManagedServices), declared)}, nil
			},
			apply: func(ctx context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, options Options, system System) error {
				return applyServices(ctx, plan, options, system)
			},
			clean: func(ctx context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, _ Options, system System) error {
				return cleanServices(ctx, plan.Extra, system)
			},
		},
		{
			name: "executable_links", label: "EXECUTABLE LINKS", stateKey: state.ManagedExecutableLinks,
			plan: func(plan *Plan) *StagePlan { return &plan.ExecutableLinks },
			build: func(_ context.Context, m *manifest.Manifest, s *state.State, options Options, _ System) (StagePlan, error) {
				return planExecutableLinks(m, s, options)
			},
			cleanPlan: func(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
				declared, err := declaredExecutableLinks(m, s, options)
				if err != nil {
					return StagePlan{}, err
				}
				return StagePlan{Extra: subtract(s.Items(state.ManagedKey, state.ManagedExecutableLinks), declared)}, nil
			},
			apply: func(_ context.Context, plan StagePlan, m *manifest.Manifest, s *state.State, options Options, _ System) error {
				return applyExecutableLinks(plan, options, m, s)
			},
			clean: func(_ context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, _ Options, _ System) error {
				return removeExecutableLinks(plan.Extra)
			},
		},
		{
			name: "directories", label: "DIRECTORIES", stateKey: state.ManagedDirectories,
			plan: func(plan *Plan) *StagePlan { return &plan.Directories },
			build: func(_ context.Context, m *manifest.Manifest, s *state.State, options Options, _ System) (StagePlan, error) {
				return planDirectories(m, s, options)
			},
			cleanPlan: func(m *manifest.Manifest, s *state.State, options Options) (StagePlan, error) {
				declared, err := declaredDirectories(m, s, options)
				if err != nil {
					return StagePlan{}, err
				}
				return StagePlan{Extra: subtract(s.Items(state.ManagedKey, state.ManagedDirectories), declared)}, nil
			},
			apply: func(_ context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, _ Options, _ System) error {
				if err := applyDirectories(plan); err != nil {
					return err
				}
				return cleanDirectories(plan.Extra)
			},
			clean: func(_ context.Context, plan StagePlan, _ *manifest.Manifest, _ *state.State, _ Options, _ System) error {
				return cleanDirectories(plan.Extra)
			},
		},
	}
}

func (p Plan) Stages() []NamedStage {
	definitions := resourceStages()
	stages := make([]NamedStage, 0, len(definitions))
	for _, definition := range definitions {
		stages = append(stages, NamedStage{
			Name:         definition.name,
			Label:        definition.label,
			IncludeEmpty: definition.includeEmpty,
			Plan:         *definition.plan(&p),
		})
	}
	return stages
}

type plannedResourceStage struct {
	definition resourceStage
	plan       StagePlan
}

func findPlannedStage(stages []plannedResourceStage, name string) (*plannedResourceStage, error) {
	for index := range stages {
		if stages[index].definition.name == name {
			return &stages[index], nil
		}
	}
	return nil, fmt.Errorf("resource stage is not registered: %s", name)
}
