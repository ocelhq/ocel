package deploy

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const routeTableSetName = "route-table"

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
	manifest.add(cfg.CacheStoreBucket, key, int64(len(table.Table)))
	return &assetSet{
		name:   routeTableSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, progress progress.Log) error {
			phaseStart := time.Now()
			stats := newUploadBatchStats()
			say(progress, "Uploading "+app+"'s route table to bucket "+cfg.CacheStoreBucket)
			err := tracedPut(ctx, cfg.CacheStoreObjects, cfg.CacheStoreBucket, key, objectHeaders{contentType: "application/json"}, table.Table, stats)
			emitUploadBatch(progress, uploadKindRouteTable, stats, err, phaseStart)
			return err
		},
	}, nil
}
