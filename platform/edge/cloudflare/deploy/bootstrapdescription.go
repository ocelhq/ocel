package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func (p *cloudflare) describeBootstrap(ctx context.Context, tier environment.Tier) ([]edge.BootstrapPart, error) {
	accountID, err := bootstrapCredentials()
	if err != nil {
		return nil, err
	}
	state, err := p.readState(ctx, accountID, tier)
	if err != nil {
		return nil, err
	}
	var parts []edge.BootstrapPart
	for _, change := range state.changes() {
		current := change.Action == edge.PlanKeep
		at := -1
		for i, part := range parts {
			if part.Name == change.Name {
				at = i
				break
			}
		}
		if at < 0 {
			parts = append(parts, edge.BootstrapPart{Name: change.Name, Current: current})
			continue
		}
		parts[at].Current = parts[at].Current && current
	}
	return parts, nil
}
