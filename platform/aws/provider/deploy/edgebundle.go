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

func publishEdgeBundle(ctx context.Context, cfg Config, app string, coord naming.Coordinate, sealed appBundle, report providerkit.Reporter) error {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil
	}
	phaseStart := time.Now()
	stats := newUploadBatchStats()
	err := putEdgeBundle(ctx, cfg, app, coord, sealed, stats, report)
	emitUploadBatch(report, uploadKindEdgeBundle, stats, err, phaseStart)
	return err
}

func putEdgeBundle(ctx context.Context, cfg Config, app string, coord naming.Coordinate, sealed appBundle, stats *uploadBatchStats, report providerkit.Reporter) error {
	bundle, ok, err := readEdgeBundle(cfg, app)
	if err != nil {
		return recordUploadFailure(stats, err)
	}
	if !ok {
		return nil
	}
	report.Say("Uploading " + app + "'s edge bundle")
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

func recordUploadFailure(stats *uploadBatchStats, err error) error {
	now := time.Now()
	stats.record(uploadOutcome{Start: now, End: now, Failed: true, Err: err})
	return err
}

func loaderID(bundle []byte, compatDate string, compatFlags []string) string {
	h := sha256.New()
	h.Write(bundle)
	h.Write([]byte(compatDate))
	h.Write([]byte(strings.Join(compatFlags, ",")))
	return hex.EncodeToString(h.Sum(nil))
}
