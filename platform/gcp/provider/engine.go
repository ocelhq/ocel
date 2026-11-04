package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
)

type engine struct{ p *Provider }

func (e engine) EnsureTierKeys(ctx context.Context, tier environment.Tier, addedBy string) error {
	return e.p.ensureKeys(ctx, tier, addedBy)
}

func (e engine) EnsureProjectDatabaseRecorded(ctx context.Context, tier environment.Tier, project string) error {
	return e.p.ensureProjectDatabaseRecorded(ctx, tier, project)
}
