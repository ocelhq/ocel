package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func refusePlanningRetirement(context.Context, provider.LiveApps, progress.Log) (provider.Plan, error) {
	return provider.Plan{}, provider.RefuseNeedsRunner("planning workers' retirement")
}

func refuseRetiring(context.Context, provider.LiveApps, progress.Log) error {
	return provider.RefuseNeedsRunner("retiring workers")
}
