package site

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const RouteTableSetName = "route-table"

func selectEdgeObjectTarget(b Buckets) uploadTarget {
	return uploadTarget{up: b.Objects, bucket: b.Assets, tier: b.Tier, edgeReadable: true}
}

func routeTableSet(b Buckets, app string, table *provider.EdgeRouteTable) (*AssetSet, error) {
	if table == nil {
		return nil, nil
	}
	if !b.CanEdgeRead() {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"%s is routed at the edge by its route table, and this account holds no credential for that edge to read the asset bucket with. Re-run `%s`",
			app, provider.BootstrapCommand(b.Tier))
	}
	key := table.Location.Key
	manifest := newSetManifest()
	to := selectEdgeObjectTarget(b)
	manifest.add(to.bucket, key, int64(len(table.Table)))
	return &AssetSet{
		name:   RouteTableSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, progress progress.Log) error {
			phaseStart := time.Now()
			stats := newUploadBatchStats()
			say(progress, "Uploading "+app+"'s route table to bucket "+to.bucket)
			err := tracedPut(ctx, to.up, to.bucket, key, objectHeaders{contentType: "application/json"}.taggedFor(to), table.Table, stats)
			emitUploadBatch(progress, uploadKindRouteTable, stats, err, phaseStart)
			return err
		},
	}, nil
}
