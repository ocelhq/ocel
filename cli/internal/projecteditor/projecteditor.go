package projecteditor

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/cli/node"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func Serve(ctx context.Context, cfg *project.Project, provider *providerprocess.Provider, tier environmentv1.Tier, declarations *variables.Declarations, recovery *variableeditor.Recovery) (*variableeditor.Session, error) {
	assets, err := node.VariableEditor()
	if err != nil {
		return nil, fmt.Errorf("read the bundled variables UI: %w", err)
	}

	store := valuestore.Store{Provider: provider, Project: cfg, Tier: tier}

	var environments []string
	if tier == environmentv1.Tier_TIER_PREVIEW {
		if environments, err = valuestore.ListEnvironmentNames(ctx, provider, cfg.Slug); err != nil {
			return nil, err
		}
	}

	return variableeditor.Serve(ctx, variableeditor.Options{
		Assets:       assets,
		Declarations: declarations,
		Values:       store,
		OtherValues:  valuestore.Store{Provider: provider, Project: cfg, Tier: variablescope.OtherTier(tier)},
		EnvSource:    store,
		Slug:         cfg.Slug,
		Tier:         tier,
		Environments: environments,
		Recovery:     recovery,
	})
}
