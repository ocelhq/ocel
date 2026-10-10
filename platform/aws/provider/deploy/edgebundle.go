package deploy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

const edgeSealedFile = "sealed.bin"

const edgeKind = "edge"

func appEdgePrefix(c naming.Coordinate) string {
	return c.StoragePrefix() + edgeKind
}

func appEdgeBundleKey(c naming.Coordinate) string {
	return path.Join(appEdgePrefix(c), "bundle.json")
}

func appEdgeSealedKey(c naming.Coordinate) string {
	return path.Join(appEdgePrefix(c), edgeSealedFile)
}

func readEdgeBundle(cfg Config, app string) ([]byte, bool, error) {
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), filepath.FromSlash(edge.AppBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read edge bundle for %s: %w", app, err)
	}
	return raw, true, nil
}

func edgeSealedDelivered(cfg Config, bundle appBundle) bool {
	return cfg.CacheStoreBucket != "" && cfg.CacheStoreObjects != nil && len(bundle.Ciphertext) > 0
}

type edgeDelivery struct {
	BundleKey     string
	RouteTableKey string
	Envelope      string
}

func edgeBundleSet(cfg Config, app string, coord naming.Coordinate, sealed appBundle) (*assetSet, edgeDelivery, error) {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreObjects == nil {
		return nil, edgeDelivery{}, nil
	}
	bundle, present, err := readEdgeBundle(cfg, app)
	if err != nil {
		return nil, edgeDelivery{}, err
	}
	if !present {
		return nil, edgeDelivery{}, nil
	}
	delivery := edgeDelivery{BundleKey: appEdgeBundleKey(coord)}
	manifest := newSetManifest()
	to := selectEdgeObjectTarget(cfg)
	manifest.add(to.bucket, appEdgeBundleKey(coord), int64(len(bundle)))
	if edgeSealedDelivered(cfg, sealed) {
		manifest.add(to.bucket, appEdgeSealedKey(coord), int64(len(sealed.Ciphertext)))
		delivery.Envelope = sealed.Envelope
	}

	return &assetSet{
		name:   edgeBundleSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, progress progress.Log) error {
			phaseStart := time.Now()
			stats := newUploadBatchStats()
			err := putEdgeBundle(ctx, cfg, app, coord, bundle, sealed, stats, progress)
			emitUploadBatch(progress, uploadKindEdgeBundle, stats, err, phaseStart)
			return err
		},
	}, delivery, nil
}

func putEdgeBundle(ctx context.Context, cfg Config, app string, coord naming.Coordinate, bundle []byte, sealed appBundle, stats *uploadBatchStats, progress progress.Log) error {
	to := selectEdgeObjectTarget(cfg)
	say(progress, "Uploading "+app+"'s edge bundle to bucket "+to.bucket)
	if err := tracedPut(ctx, to.up, to.bucket, appEdgeBundleKey(coord), objectHeaders{contentType: "application/json"}.taggedFor(to), bundle, stats); err != nil {
		return err
	}
	if !edgeSealedDelivered(cfg, sealed) {
		return nil
	}
	return tracedPut(ctx, to.up, to.bucket, appEdgeSealedKey(coord), objectHeaders{contentType: "application/octet-stream"}.taggedFor(to), sealed.Ciphertext, stats)
}

func checkAppEdgeVariables(cfg Config, app string, values provider.AppValues, bundle appBundle) error {
	_, ok, err := readEdgeBundle(cfg, app)
	if err != nil || !ok {
		return err
	}
	return checkEdgeVariables(app, values, bundle.Ciphertext)
}
