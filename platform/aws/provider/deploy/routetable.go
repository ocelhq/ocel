package deploy

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const routeTableSetName = "route-table"

func selectEdgeObjectTarget(cfg Config) uploadTarget {
	return uploadTarget{up: cfg.Objects, bucket: cfg.AssetBucket, tier: cfg.Tier, edgeReadable: true}
}

func routeTableSet(cfg Config, app string, table *provider.EdgeRouteTable) (*assetSet, error) {
	if table == nil {
		return nil, nil
	}
	if !edgeReadsAssetBucket(cfg) {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"%s is routed at the edge by its route table, and this account holds no credential for that edge to read the asset bucket with. Re-run `%s`",
			app, provider.BootstrapCommand(cfg.Tier))
	}
	key := table.Location.Key
	manifest := newSetManifest()
	to := selectEdgeObjectTarget(cfg)
	manifest.add(to.bucket, key, int64(len(table.Table)))
	return &assetSet{
		name:   routeTableSetName,
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
