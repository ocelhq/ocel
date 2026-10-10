package deploy

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const routeTableSetName = "route-table"

func edgeObjectTargets(cfg Config) []uploadTarget {
	targets := []uploadTarget{{up: cfg.CacheStoreObjects, bucket: cfg.CacheStoreBucket, tier: cfg.Tier}}
	if cfg.AssetBucket != "" && cfg.Objects != nil {
		targets = append(targets, uploadTarget{up: cfg.Objects, bucket: cfg.AssetBucket, tier: cfg.Tier})
	}
	return targets
}

func routeTableSet(cfg Config, app string, table *provider.EdgeRouteTable) (*assetSet, error) {
	if table == nil {
		return nil, nil
	}
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreObjects == nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"%s is routed at the edge by its route table, and this account adopted no cache store from that edge to keep it in. Re-run `%s`",
			app, provider.BootstrapCommand(cfg.Tier))
	}
	key := table.Location.Key
	manifest := newSetManifest()
	targets := edgeObjectTargets(cfg)
	for _, to := range targets {
		manifest.add(to.bucket, key, int64(len(table.Table)))
	}
	return &assetSet{
		name:   routeTableSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, progress progress.Log) error {
			phaseStart := time.Now()
			stats := newUploadBatchStats()
			say(progress, "Uploading "+app+"'s route table to bucket "+cfg.CacheStoreBucket)
			var err error
			for _, to := range targets {
				if err = tracedPut(ctx, to.up, to.bucket, key, objectHeaders{contentType: "application/json"}, table.Table, stats); err != nil {
					break
				}
			}
			emitUploadBatch(progress, uploadKindRouteTable, stats, err, phaseStart)
			return err
		},
	}, nil
}
