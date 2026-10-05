package telemetry

import (
	"maps"
	"slices"
	"time"
)

const (
	DeployTargetProduction = "production"
	DeployTargetPreview    = "preview"
)

type PlanActions struct {
	Create int
	Update int
	Delete int
}

type DeployCompletion struct {
	Success        bool
	Target         string
	Provider       string
	Frameworks     []string
	Languages      []string
	AppCount       int
	ResourceCounts map[string]int
	PlanActions    PlanActions
	PhaseDurations map[string]time.Duration
	FirstDeploy    bool
	ErrorCode      string
	Assumed        []string
}

func (DeployCompletion) name() string { return "deploy_completed" }

func (completion DeployCompletion) properties() map[string]any {
	phaseMS := make(map[string]int64, len(completion.PhaseDurations))
	for phase, duration := range completion.PhaseDurations {
		phaseMS[phase] = duration.Milliseconds()
	}
	resourceCounts := maps.Clone(completion.ResourceCounts)
	if resourceCounts == nil {
		resourceCounts = map[string]int{}
	}
	return map[string]any{
		"success":         completion.Success,
		"target":          completion.Target,
		"provider":        completion.Provider,
		"frameworks":      sortedUnique(completion.Frameworks),
		"languages":       sortedUnique(completion.Languages),
		"app_count":       completion.AppCount,
		"resource_counts": resourceCounts,
		"plan_actions": map[string]int{
			"create": completion.PlanActions.Create,
			"update": completion.PlanActions.Update,
			"delete": completion.PlanActions.Delete,
		},
		"phase_ms":     phaseMS,
		"first_deploy": completion.FirstDeploy,
		"error_code":   completion.ErrorCode,
		"assumed":      sortedUnique(completion.Assumed),
	}
}

func sortedUnique(values []string) []string {
	return slices.Compact(append([]string{}, slices.Sorted(slices.Values(values))...))
}
