package site

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
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

const edgeKind = "edge"

func appEdgePrefix(c naming.Coordinate) string {
	return c.StoragePrefix() + edgeKind
}

func appEdgeBundleKey(c naming.Coordinate) string {
	return path.Join(appEdgePrefix(c), "bundle.json")
}

func appEdgeSealedKey(c naming.Coordinate) string {
	return path.Join(appEdgePrefix(c), variables.EdgeSealedFile)
}

func readEdgeBundle(root, app string) ([]byte, bool, error) {
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(root, app), filepath.FromSlash(edge.AppBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read edge bundle for %s: %w", app, err)
	}
	return raw, true, nil
}

func edgeSealedDelivered(b Buckets, bundle variables.Bundle) bool {
	return b.CanEdgeRead() && len(bundle.Ciphertext) > 0
}

type EdgeDelivery struct {
	BundleKey     string
	RouteTableKey string
	Envelope      string
}

func edgeBundleSet(b Buckets, root, app string, coord naming.Coordinate, sealed variables.Bundle) (*AssetSet, EdgeDelivery, error) {
	if !b.CanEdgeRead() {
		return nil, EdgeDelivery{}, nil
	}
	bundle, present, err := readEdgeBundle(root, app)
	if err != nil {
		return nil, EdgeDelivery{}, err
	}
	if !present {
		return nil, EdgeDelivery{}, nil
	}
	delivery := EdgeDelivery{BundleKey: appEdgeBundleKey(coord)}
	manifest := newSetManifest()
	to := selectEdgeObjectTarget(b)
	manifest.add(to.bucket, appEdgeBundleKey(coord), int64(len(bundle)))
	if edgeSealedDelivered(b, sealed) {
		manifest.add(to.bucket, appEdgeSealedKey(coord), int64(len(sealed.Ciphertext)))
		delivery.Envelope = sealed.Envelope
	}

	return &AssetSet{
		name:   EdgeBundleSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, progress progress.Log) error {
			phaseStart := time.Now()
			stats := newUploadBatchStats()
			err := putEdgeBundle(ctx, b, app, coord, bundle, sealed, stats, progress)
			emitUploadBatch(progress, uploadKindEdgeBundle, stats, err, phaseStart)
			return err
		},
	}, delivery, nil
}

func putEdgeBundle(ctx context.Context, b Buckets, app string, coord naming.Coordinate, bundle []byte, sealed variables.Bundle, stats *uploadBatchStats, progress progress.Log) error {
	to := selectEdgeObjectTarget(b)
	say(progress, "Uploading "+app+"'s edge bundle to bucket "+to.bucket)
	if err := tracedPut(ctx, to.up, to.bucket, appEdgeBundleKey(coord), objectHeaders{contentType: "application/json"}.taggedFor(to), bundle, stats); err != nil {
		return err
	}
	if !edgeSealedDelivered(b, sealed) {
		return nil
	}
	return tracedPut(ctx, to.up, to.bucket, appEdgeSealedKey(coord), objectHeaders{contentType: "application/octet-stream"}.taggedFor(to), sealed.Ciphertext, stats)
}

func CheckEdgeVariables(root, app string, values provider.AppValues, bundle variables.Bundle) error {
	_, ok, err := readEdgeBundle(root, app)
	if err != nil || !ok {
		return err
	}
	return variables.CheckEdgeVariables(app, values, bundle.Ciphertext)
}
