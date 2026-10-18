package gcp

import (
	"context"
	"fmt"

	"cloud.google.com/go/storage"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func (p *Provider) putEdgeRouteTable(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (string, error) {
	if spec.App == nil || spec.App.EdgeRouteTable == nil {
		return "", nil
	}
	if spec.Edge == nil {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s is routed at the edge by its route table, and its stack names no edge to keep it", spec.App.App)
	}
	table := spec.App.EdgeRouteTable
	gcloud.EnsureProgress(runProgress).Say("Uploading " + spec.App.App + "'s route table to the " + provider.StoreAssets + " store")
	attrs := storage.ObjectAttrs{ContentType: "application/json"}
	if err := p.createObject(ctx, spec.Ref.Tier, provider.StoreAssets, table.Location.Key, attrs, table.Table); err != nil {
		return "", fmt.Errorf("upload %s's route table: %w", spec.App.App, err)
	}
	return table.Location.Key, nil
}
