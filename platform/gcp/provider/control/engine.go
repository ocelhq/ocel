package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Engine interface {
	EnsureTierKeys(ctx context.Context, tier environment.Tier, addedBy string) error
	EnsureProjectDatabaseRecorded(ctx context.Context, tier environment.Tier, project string) error
}

type needsRunner struct{}

func (needsRunner) EnsureTierKeys(_ context.Context, tier environment.Tier, _ string) error {
	return provider.RefuseNeedsRunner("adding the " + string(tier) + " tier's encryption key")
}

func (needsRunner) EnsureProjectDatabaseRecorded(_ context.Context, _ environment.Tier, project string) error {
	return provider.RefuseNeedsRunner("recording the database of project " + project)
}
