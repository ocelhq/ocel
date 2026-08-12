package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const edgeBundleFile = "edge/bundle.json"

const edgeKind = "edge"

func appEdgePrefix(c naming.Coordinate) string {
	return c.StoragePrefix() + edgeKind
}

func appEdgeBundleKey(c naming.Coordinate) string {
	return path.Join(appEdgePrefix(c), "bundle.json")
}

func readEdgeBundle(cfg Config, app string) ([]byte, bool, error) {
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), filepath.FromSlash(edgeBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read edge bundle for %s: %w", app, err)
	}
	return raw, true, nil
}

func uploadEdgeBundles(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, builds appBuilds) error {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil
	}
	phaseStart := time.Now()
	stats := newUploadBatchStats()
	err := putEdgeBundles(ctx, cfg, manifest, builds, stats)
	emitUploadBatch(cfg.Tracer, cfg.Stages.Uploading.ID, uploadKindEdgeBundle, stats, err, phaseStart)
	return err
}

func putEdgeBundles(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, builds appBuilds, stats *uploadBatchStats) error {
	for _, app := range manifestApps(manifest) {
		name := app.GetName()
		bundle, ok, err := readEdgeBundle(cfg, name)
		if err != nil {
			now := time.Now()
			stats.record(uploadOutcome{Start: now, End: now, Failed: true, Err: err})
			return err
		}
		if !ok {
			continue
		}
		key := appEdgeBundleKey(builds.coords[name])
		if err := tracedPut(ctx, cfg.CacheStoreUploader, cfg.CacheStoreBucket, key, "application/json", bundle, stats); err != nil {
			return err
		}
	}
	return nil
}

func appEdgeWorkers(cfg Config, c naming.Coordinate, app string) (*edge.Code, error) {
	loader, ok := cfg.Edge.(edge.CodeLoader)
	if !ok {
		return nil, nil
	}
	bundle, ok, err := readEdgeBundle(cfg, app)
	if err != nil || !ok {
		return nil, err
	}
	compatDate, compatFlags := loader.CodeRuntime()
	return &edge.Code{
		BundleKey:   appEdgeBundleKey(c),
		ID:          loaderID(bundle, compatDate, compatFlags),
		CompatDate:  compatDate,
		CompatFlags: compatFlags,
	}, nil
}

func loaderID(bundle []byte, compatDate string, compatFlags []string) string {
	h := sha256.New()
	h.Write(bundle)
	h.Write([]byte(compatDate))
	h.Write([]byte(strings.Join(compatFlags, ",")))
	return hex.EncodeToString(h.Sum(nil))
}
