package variables

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type ProjectRuns func(ctx context.Context, tier environment.Tier, project string) error

type projectStores struct {
	takeDatabase func(ctx context.Context, tier environment.Tier, id string) error
	takeRuns     ProjectRuns
}

func storesOf(b bootstrap.Engine, takeRuns ProjectRuns) projectStores {
	return projectStores{takeDatabase: b.TakeTierDatabase, takeRuns: takeRuns}
}

func PlanRemoveProject(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string) ([]provider.Change, error) {
	return planRemoveProject(ctx, b, tier, project)
}

func RemoveProject(ctx context.Context, b bootstrap.Engine, takeRuns ProjectRuns, tier environment.Tier, project string, log progress.Log) error {
	return removeProject(ctx, b, storesOf(b, takeRuns), tier, project, log)
}

func planRemoveProject(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string) ([]provider.Change, error) {
	recorded, found, err := ports.ReadProjectDatabaseRecord(ctx, b.Records, tier, project)
	if err != nil || !found || recorded == b.Clients.SharedDatabase(tier) {
		return nil, err
	}
	return []provider.Change{deleted(bootstrap.KindDatabase, recorded, "")}, nil
}

func removeProject(ctx context.Context, b bootstrap.Engine, stores projectStores, tier environment.Tier, project string, log progress.Log) error {
	return takeProjectDatabase(ctx, b, stores, tier, project, log)
}

func deleted(kind bootstrap.Kind, name, reason string) provider.Change {
	return provider.Change{Kind: string(kind), Name: name, Action: provider.ActionDelete, Reason: reason}
}
