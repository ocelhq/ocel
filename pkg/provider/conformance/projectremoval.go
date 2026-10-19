package conformance

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func projectRemovalFaults(ctx context.Context, hooks *provider.ProjectRemovalHooks) []string {
	if hooks.Plan == nil || hooks.Remove == nil {
		return []string{"ProjectRemoval leaves Plan or Remove nil, and a project is removed only as its plan showed"}
	}
	tier := environment.TierPreview
	var faults []string
	groups, err := hooks.Plan(ctx, tier, untouchedProject)
	if err != nil {
		faults = append(faults, fmt.Sprintf("Plan() of a project nothing deployed = %v, want nothing to remove", err))
	} else if len(groups) != 0 {
		faults = append(faults, fmt.Sprintf("Plan() of a project nothing deployed = %+v, want nothing to remove where nothing was deployed", groups))
	}
	for attempt := range 2 {
		if err := hooks.Remove(ctx, tier, untouchedProject, progress.Discard()); err != nil {
			faults = append(faults, fmt.Sprintf("Remove() number %d of a project nothing deployed = %v, want a rerun removal to pass over what is already gone", attempt+1, err))
		}
	}
	return faults
}
