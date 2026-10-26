package cloudflare

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
)

func (p *cloudflare) retireFronting(ctx context.Context, accountID string, prior edge.StackState) (edge.StackState, error) {
	var recorded private
	if err := prior.Private.Into(&recorded); err != nil {
		return prior, err
	}
	if recorded.EntryWorker == "" && recorded.FrontedBy == "" {
		return prior, nil
	}
	names, err := p.resolveNames(ctx)
	if err != nil {
		return prior, err
	}
	snap := p.routeSnapshot()
	if recorded.FrontedBy != "" {
		for _, host := range recorded.Fronted {
			if err := p.repointHostRoute(ctx, snap, accountID, host, recorded.FrontedBy, ""); err != nil {
				return prior, err
			}
		}
	}
	if recorded.EntryWorker != "" {
		up := upload{accountID: accountID, scriptName: recorded.EntryWorker}
		if err := p.pruneStaleRoutes(ctx, snap, names.sharedPreviewEntry(), up, recorded.Routed, nil); err != nil {
			return prior, err
		}
		if err := p.deleteScript(ctx, accountID, recorded.EntryWorker); err != nil {
			return prior, fmt.Errorf("delete the entry worker %q the edge no longer fronts this project with: %w", recorded.EntryWorker, err)
		}
	}
	if err := p.destroyInstance(ctx, prior); err != nil {
		return prior, fmt.Errorf("destroy the releases-store instance the entry worker read: %w", err)
	}
	prior.Private = edge.Private{}
	prior.Secret, prior.Endpoint, prior.PurgeURL = "", "", ""
	return prior, nil
}
