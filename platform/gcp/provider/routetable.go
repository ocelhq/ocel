package gcp

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (p *Provider) putEdgeRouteTable(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (string, error) {
	if spec.App == nil || spec.App.EdgeRouteTable == nil {
		return "", nil
	}
	if spec.Edge == nil {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s is routed at the edge by its route table, and its stack names no edge to keep it", spec.App.App)
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return "", err
	}
	store, adopted, err := readAdoptedCacheStore(ctx, c, p.KeyValues(), spec.Ref.Tier, spec.Edge.Kind())
	if err != nil {
		return "", err
	}
	if !adopted {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"%s is routed at the edge by its route table, and this project adopted no cache store from the %s edge to keep it in. Re-run `%s`",
			spec.App.App, spec.Edge.Kind(), provider.BootstrapCommand(spec.Ref.Tier))
	}
	table := spec.App.EdgeRouteTable
	ensureProgress(runProgress).Say("Uploading " + spec.App.App + "'s route table to bucket " + store.bucket)
	if err := store.put(ctx, table.Location.Key, "application/json", table.Table); err != nil {
		return "", fmt.Errorf("upload %s's route table: %w", spec.App.App, err)
	}
	return table.Location.Key, nil
}
