package reconcile

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/lukelex/dotpkg/internal/resource"
)

type PlanChange struct {
	Action string `json:"action"`
	Item   string `json:"item"`
}

type PlanStage struct {
	Name     string       `json:"name"`
	Declared []string     `json:"declared"`
	Adopted  []string     `json:"adopted"`
	Missing  []string     `json:"missing"`
	Extra    []string     `json:"extra"`
	Changes  []PlanChange `json:"changes"`
}

// PlanDocument is the stable machine-readable plan envelope. The schema name
// and version are intentionally explicit so callers can reject incompatible
// output instead of guessing from fields.
type PlanDocument struct {
	Schema   string      `json:"schema"`
	Version  int         `json:"version"`
	Manifest string      `json:"manifest"`
	Profile  string      `json:"profile"`
	Host     string      `json:"host,omitempty"`
	Changes  int         `json:"changes"`
	Stages   []PlanStage `json:"stages"`
}

func NewPlanDocument(packagePlan Plan, resourcePlan *resource.Plan, options Options, appImagePlans ...AppImagePlan) PlanDocument {
	document := PlanDocument{
		Schema:   "dotpkg.plan",
		Version:  1,
		Manifest: options.ManifestPath,
		Profile:  options.Profile,
		Host:     options.HostLabel,
		Stages:   []PlanStage{planStage("packages", packagePlan.Declared, packagePlan.Adopted, packagePlan.Missing, packagePlan.Extra, "install")},
	}
	if len(appImagePlans) > 0 {
		plan := appImagePlans[0]
		document.Stages = append(document.Stages, planStage("appimages", plan.Declared, plan.Adopted, plan.Missing, plan.Extra, "install"))
	}
	if document.Host == "" {
		document.Host = options.HostPath
	}
	if resourcePlan != nil {
		for _, stage := range resourcePlan.Stages() {
			if stage.IncludeEmpty || stage.Plan.Changes() > 0 || len(stage.Plan.Declared) > 0 {
				document.Stages = append(document.Stages, resourceStage(stage.Name, stage.Plan))
			}
		}
	}
	for _, stage := range document.Stages {
		document.Changes += len(stage.Changes)
	}
	return document
}

func appendGitHubPlan(document *PlanDocument, plan GitHubPlan) {
	document.Stages = append(document.Stages, planStage("github", plan.Declared, plan.Adopted, plan.Missing, plan.Extra, "install"))
	document.Changes += plan.Changes()
}

func planStage(name string, declared, adopted, missing, extra []string, missingAction string) PlanStage {
	stage := PlanStage{
		Name:     name,
		Declared: copyStrings(declared),
		Adopted:  copyStrings(adopted),
		Missing:  copyStrings(missing),
		Extra:    copyStrings(extra),
		Changes:  make([]PlanChange, 0, len(adopted)+len(missing)+len(extra)),
	}
	for _, item := range adopted {
		stage.Changes = append(stage.Changes, PlanChange{Action: "adopt", Item: item})
	}
	for _, item := range missing {
		stage.Changes = append(stage.Changes, PlanChange{Action: missingAction, Item: item})
	}
	for _, item := range extra {
		stage.Changes = append(stage.Changes, PlanChange{Action: "remove", Item: item})
	}
	return stage
}

func resourceStage(name string, plan resource.StagePlan) PlanStage {
	return planStage(name, plan.Declared, plan.Adopted, plan.Missing, plan.Extra, "add")
}

func copyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string{}, values...)
}

func printPlan(output io.Writer, plan Plan, format string, resourcePlan *resource.Plan, options Options, appImagePlans ...AppImagePlan) {
	if format == "json" {
		if len(appImagePlans) > 0 {
			_ = json.NewEncoder(output).Encode(NewPlanDocument(plan, resourcePlan, options, appImagePlans[0]))
		} else {
			_ = json.NewEncoder(output).Encode(NewPlanDocument(plan, resourcePlan, options))
		}
		return
	}
	printPackagePlan(output, "PACKAGES", plan, "install")
	if len(appImagePlans) > 0 && appImagePlans[0].Changes() > 0 {
		printPackagePlan(output, "APPIMAGES", appImagePlans[0].Plan, "install")
	}
	if resourcePlan != nil {
		for _, stage := range resourcePlan.Stages() {
			if len(stage.Plan.Extra) == 0 {
				continue
			}
			fmt.Fprintln(output, stage.Label)
			for _, item := range stage.Plan.Extra {
				fmt.Fprintf(output, "  - remove: %s\n", item)
			}
		}
	}
}

func printPlanWithGitHub(output io.Writer, packagePlan Plan, format string, resourcePlan *resource.Plan, options Options, appImagePlan AppImagePlan, githubPlan GitHubPlan) {
	if format == "json" {
		document := NewPlanDocument(packagePlan, resourcePlan, options, appImagePlan)
		appendGitHubPlan(&document, githubPlan)
		_ = json.NewEncoder(output).Encode(document)
		return
	}
	printPlan(output, packagePlan, format, resourcePlan, options, appImagePlan)
	if githubPlan.Changes() > 0 {
		printPackagePlan(output, "GITHUB", githubPlan.Plan, "install")
	}
}

func printPackagePlan(output io.Writer, label string, plan Plan, missingAction string) {
	fmt.Fprintln(output, label)
	for _, item := range plan.Adopted {
		fmt.Fprintf(output, "  ~ adopt: %s\n", item)
	}
	for _, item := range plan.Missing {
		fmt.Fprintf(output, "  + %s: %s\n", missingAction, item)
	}
	for _, item := range plan.Extra {
		fmt.Fprintf(output, "  - remove: %s\n", item)
	}
}
