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

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	return cfg.CacheStoreBucket != "" && cfg.CacheStoreUploader != nil && len(bundle.Ciphertext) > 0
}

type edgeDelivery struct {
	BundleKey string
	Envelope  string
}

func edgeBundleSet(cfg Config, app string, coord naming.Coordinate, sealed appBundle) (*assetSet, edgeDelivery, error) {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil, edgeDelivery{}, nil
	}
	bundle, held, err := readEdgeBundle(cfg, app)
	if err != nil {
		return nil, edgeDelivery{}, err
	}
	if !held {
		return nil, edgeDelivery{}, nil
	}
	delivery := edgeDelivery{BundleKey: appEdgeBundleKey(coord)}
	manifest := newSetManifest()
	manifest.add(cfg.CacheStoreBucket, appEdgeBundleKey(coord), int64(len(bundle)))
	if edgeSealedDelivered(cfg, sealed) {
		manifest.add(cfg.CacheStoreBucket, appEdgeSealedKey(coord), int64(len(sealed.Ciphertext)))
		delivery.Envelope = sealed.Envelope
	}

	return &assetSet{
		name:   edgeBundleSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, report providerkit.Reporter) error {
			phaseStart := time.Now()
			stats := newUploadBatchStats()
			err := putEdgeBundle(ctx, cfg, app, coord, bundle, sealed, stats, report)
			emitUploadBatch(report, uploadKindEdgeBundle, stats, err, phaseStart)
			return err
		},
	}, delivery, nil
}

func putEdgeBundle(ctx context.Context, cfg Config, app string, coord naming.Coordinate, bundle []byte, sealed appBundle, stats *uploadBatchStats, report providerkit.Reporter) error {
	say(report, "Uploading "+app+"'s edge bundle")
	if err := tracedPut(ctx, cfg.CacheStoreUploader, cfg.CacheStoreBucket, appEdgeBundleKey(coord), objectHeaders{contentType: "application/json"}, bundle, stats); err != nil {
		return err
	}
	if !edgeSealedDelivered(cfg, sealed) {
		return nil
	}
	return tracedPut(ctx, cfg.CacheStoreUploader, cfg.CacheStoreBucket, appEdgeSealedKey(coord), objectHeaders{contentType: "application/octet-stream"}, sealed.Ciphertext, stats)
}

func checkAppEdgeVariables(cfg Config, app string, values providerkit.AppValues, bundle appBundle) error {
	_, ok, err := readEdgeBundle(cfg, app)
	if err != nil || !ok {
		return err
	}
	return checkEdgeVariables(app, values, bundle.Ciphertext)
}
