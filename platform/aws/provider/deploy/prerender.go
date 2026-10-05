package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/prerender"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func storageCoordinate(env, slug, app string, release naming.Release) naming.Coordinate {
	return naming.Coordinate{
		Project: naming.Sanitize(slug),
		Env:     naming.Sanitize(env),
		App:     naming.Sanitize(app),
		Release: release,
	}
}

func isrPrefixOf(c naming.Coordinate) string {
	return strings.TrimSuffix(c.ISRPrefix(), naming.PathSeparator)
}

func bytecodePrefixOf(c naming.Coordinate) string {
	return strings.TrimSuffix(c.BytecodePrefix(), naming.PathSeparator)
}

type prerenderUpload struct {
	key, src string
	to       uploadTarget
}

func prerenderAssetSet(cfg Config, app string, cache *isrConfig) (*assetSet, error) {
	if cache == nil {
		return nil, nil
	}

	seeds, err := prerender.Seeds(appArtifactRoot(cfg.ArtifactRoot, app), cache.Prefix)
	if err != nil {
		return nil, err
	}

	var uploads []prerenderUpload
	manifest := newSetManifest()
	targets := map[string]uploadTarget{
		prerender.EntrySegment: entryTarget(cfg),
		prerender.FetchSegment: {up: cfg.Objects, bucket: cfg.AssetBucket, tier: cfg.Tier},
	}
	for _, seed := range seeds {
		to := targets[seed.Segment]
		uploads = append(uploads, prerenderUpload{key: seed.Key, src: seed.Path, to: to})
		manifest.add(to.bucket, seed.Key, seed.Size)
	}
	if len(uploads) > 0 {
		for _, to := range targets {
			if err := to.validate(); err != nil {
				return nil, err
			}
		}
	}

	return &assetSet{
		name:   prerenderAssetSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, progress progress.Log) error {
			return pushPrerenderAssets(ctx, cfg, app, cache, uploads, progress)
		},
	}, nil
}

func pushPrerenderAssets(ctx context.Context, cfg Config, app string, cache *isrConfig, uploads []prerenderUpload, progress progress.Log) error {
	if err := seedTagSnapshot(ctx, cfg, cache, time.Now()); err != nil {
		return err
	}
	if err := seedISRWriter(ctx, cfg.isrWriter(), app, cache); err != nil {
		return err
	}
	if len(uploads) == 0 {
		return nil
	}

	say(progress, "Uploading "+app+"'s "+plural(len(uploads), "prerender cache entry", "prerender cache entries"))
	phaseStart := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	stats := newUploadBatchStats()
	for _, u := range uploads {
		g.Go(func() error {
			defer takeUploadSlot()()
			return tracedUpload(ctx, u.to.up, u.to.bucket, u.key, objectHeaders{}, func() ([]byte, error) {
				return os.ReadFile(u.src)
			}, stats)
		})
	}
	err := g.Wait()
	emitUploadBatch(progress, uploadKindPrerenderAsset, stats, err, phaseStart)
	return err
}

type uploadTarget struct {
	up     payloads.ObjectStore
	bucket string
	tier   environment.Tier
}

func (t uploadTarget) validate() error {
	if t.bucket == "" {
		return fmt.Errorf("this project has objects to publish but no asset bucket is configured; re-run `%s`", provider.BootstrapCommand(t.tier))
	}
	if t.up == nil {
		return fmt.Errorf("no asset uploader configured")
	}
	return nil
}

func seedTagSnapshot(ctx context.Context, cfg Config, cache *isrConfig, at time.Time) error {
	body, err := json.Marshal(prerender.GenesisTagSnapshot(at))
	if err != nil {
		return fmt.Errorf("encode tag snapshot: %w", err)
	}

	for _, target := range snapshotTargets(cfg) {
		key := prerender.TagSnapshotKey(cache.Prefix)
		_, err := target.up.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(target.bucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(body),
			ContentType: aws.String("application/json"),
			IfNoneMatch: aws.String("*"),
		})
		if err != nil && !isPreconditionFailed(err) {
			return fmt.Errorf("seed tag snapshot %s in %s: %w", key, target.bucket, err)
		}
	}
	return nil
}

func snapshotTargets(cfg Config) []uploadTarget {
	var targets []uploadTarget
	for _, t := range []uploadTarget{{up: cfg.Objects, bucket: cfg.AssetBucket}, entryTarget(cfg)} {
		if t.validate() != nil {
			continue
		}
		if len(targets) == 1 && targets[0].bucket == t.bucket {
			continue
		}
		targets = append(targets, t)
	}
	return targets
}

func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
		return true
	}
	var respErr *awshttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusPreconditionFailed
}

func entryTarget(cfg Config) uploadTarget {
	if isrEntriesAdopted(cfg.objectStores()) {
		return uploadTarget{up: cfg.CacheStoreObjects, bucket: cfg.CacheStoreBucket, tier: cfg.Tier}
	}
	return uploadTarget{up: cfg.Objects, bucket: cfg.AssetBucket, tier: cfg.Tier}
}

func isrEntriesAdopted(stores ObjectStores) bool {
	return stores.CacheStoreBucket != "" && stores.CacheStoreObjects != nil
}

type collectedFile struct {
	rel  string
	size int64
}

func collectFiles(dir string) ([]collectedFile, error) {
	var files []collectedFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, collectedFile{rel: filepath.ToSlash(rel), size: info.Size()})
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("crawl %s: %w", dir, err)
	}
	return files, nil
}
