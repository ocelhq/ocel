package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

func (p *Provider) ensureProjectDatabaseRecorded(ctx context.Context, tier environment.Tier, project string) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return variables.EnsureProjectDatabaseRecorded(ctx, b, tier, project, p.Options.OwnDatabase, nil, appDatabaseLayouts()...)
}
