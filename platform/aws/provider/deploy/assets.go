package deploy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const staticAssetsDir = "static"

const imageConfigFile = "image-config.json"

func appAssetPrefix(c naming.Coordinate) string {
	return c.AssetKey("")
}

func assetPlaneTargets(cfg Config) []uploadTarget {
	return []uploadTarget{
		{up: cfg.CacheStoreUploader, bucket: cfg.CacheStoreBucket},
		{up: cfg.Uploader, bucket: cfg.AssetBucket},
	}
}

func uploadStaticAssets(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, builds appBuilds) error {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil
	}

	assetBucket := uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket}
	plane := assetPlaneTargets(cfg)
	type upload struct {
		key, src    string
		to          []uploadTarget
		replace     bool
		contentType string
	}
	var uploads []upload
	for _, app := range manifestApps(manifest) {
		if app.GetFramework() != frameworkNext {
			continue
		}
		name := app.GetName()
		coord := builds.coords[name]
		root := appArtifactRoot(cfg.ArtifactRoot, name)
		dir := filepath.Join(root, staticAssetsDir)
		rels, err := collectFiles(dir)
		if err != nil {
			return err
		}
		for _, rel := range rels {
			uploads = append(uploads, upload{
				key: coord.AssetKey(rel),
				src: filepath.Join(dir, filepath.FromSlash(rel)),
				to:  plane,
			})
		}
		imageConfig := filepath.Join(root, imageConfigFile)
		switch _, err := os.Stat(imageConfig); {
		case err == nil:
			uploads = append(uploads, upload{
				key:         coord.ImageConfigKey(),
				src:         imageConfig,
				to:          []uploadTarget{assetBucket},
				replace:     true,
				contentType: "application/json",
			})
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("stat image config for %s: %w", name, err)
		}
	}
	if len(uploads) == 0 {
		return nil
	}
	if err := assetBucket.validate(); err != nil {
		return err
	}

	phaseStart := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	stats := newUploadBatchStats()
	for _, u := range uploads {
		read := sync.OnceValues(func() ([]byte, error) { return os.ReadFile(u.src) })
		for _, to := range u.to {
			g.Go(func() error {
				if u.replace {
					data, err := read()
					if err != nil {
						readErr := fmt.Errorf("read %s: %w", u.src, err)
						now := time.Now()
						stats.record(uploadOutcome{Start: now, End: now, Failed: true, Err: readErr})
						return readErr
					}
					return tracedPut(ctx, to.up, to.bucket, u.key, u.contentType, data, stats)
				}
				return tracedUpload(ctx, to.up, to.bucket, u.key, u.contentType, read, stats)
			})
		}
	}
	err := g.Wait()
	emitUploadBatch(cfg.Tracer, cfg.Stages.Uploading.ID, uploadKindStaticAsset, stats, err, phaseStart)
	return err
}
