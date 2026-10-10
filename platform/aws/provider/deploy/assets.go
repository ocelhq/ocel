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

	"github.com/ocelhq/ocel/pkg/contenttype"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
)

func assetHeaders(static *edge.Static, rel string) objectHeaders {
	return objectHeaders{contentType: contenttype.Infer(rel), cacheControl: static.CacheControl("/" + rel)}
}

const imageConfigFile = "image-config.json"

func appAssetPrefix(c naming.Coordinate) string {
	return c.AssetKey("")
}

func assetPlaneTargets(cfg Config) []uploadTarget {
	return []uploadTarget{
		{up: cfg.CacheStoreObjects, bucket: cfg.CacheStoreBucket, tier: cfg.Tier},
		{up: cfg.Objects, bucket: cfg.AssetBucket, tier: cfg.Tier, edgeReadable: true},
	}
}

type assetUpload struct {
	key, src string
	to       []uploadTarget
	replace  bool
	headers  objectHeaders
}

func staticAssetSet(cfg Config, app string, static *edge.Static, coord naming.Coordinate) (*assetSet, error) {
	if static == nil {
		return nil, nil
	}
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreObjects == nil {
		return nil, nil
	}

	assetBucket := uploadTarget{up: cfg.Objects, bucket: cfg.AssetBucket, tier: cfg.Tier}
	plane := assetPlaneTargets(cfg)
	var uploads []assetUpload
	manifest := newSetManifest()
	root := appArtifactRoot(cfg.ArtifactRoot, app)
	dir := filepath.Join(root, edge.StaticAssetDir)
	files, err := collectFiles(dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		key := coord.AssetKey(file.rel)
		uploads = append(uploads, assetUpload{
			key:     key,
			src:     filepath.Join(dir, filepath.FromSlash(file.rel)),
			to:      plane,
			headers: assetHeaders(static, file.rel),
		})
		for _, to := range plane {
			manifest.add(to.bucket, key, file.size)
		}
	}
	imageConfig := filepath.Join(root, imageConfigFile)
	switch info, err := os.Stat(imageConfig); {
	case err == nil:
		uploads = append(uploads, assetUpload{
			key:     coord.ImageConfigKey(),
			src:     imageConfig,
			to:      []uploadTarget{assetBucket},
			replace: true,
			headers: objectHeaders{contentType: "application/json"},
		})
		manifest.add(assetBucket.bucket, coord.ImageConfigKey(), info.Size())
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("stat image config for %s: %w", app, err)
	}
	if len(uploads) == 0 {
		return nil, nil
	}
	if err := assetBucket.validate(); err != nil {
		return nil, err
	}

	return &assetSet{
		name:   staticAssetSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, progress progress.Log) error {
			return pushStaticAssets(ctx, app, uploads, progress)
		},
	}, nil
}

func pushStaticAssets(ctx context.Context, app string, uploads []assetUpload, progress progress.Log) error {
	say(progress, "Uploading "+app+"'s "+plural(len(uploads), "static asset", "static assets"))
	phaseStart := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	stats := newUploadBatchStats()
	for _, u := range uploads {
		read := sync.OnceValues(func() ([]byte, error) { return os.ReadFile(u.src) })
		for _, to := range u.to {
			g.Go(func() error {
				defer takeUploadSlot()()
				if u.replace {
					data, err := read()
					if err != nil {
						readErr := fmt.Errorf("read %s: %w", u.src, err)
						now := time.Now()
						stats.record(uploadOutcome{Start: now, End: now, Failed: true, Err: readErr})
						return readErr
					}
					return tracedPut(ctx, to.up, to.bucket, u.key, u.headers.taggedFor(to), data, stats)
				}
				return tracedUpload(ctx, to.up, to.bucket, u.key, u.headers.taggedFor(to), read, stats)
			})
		}
	}
	err := g.Wait()
	emitUploadBatch(progress, uploadKindStaticAsset, stats, err, phaseStart)
	return err
}
