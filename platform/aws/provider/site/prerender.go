package site

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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/prerender"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type prerenderUpload struct {
	key, src string
	to       uploadTarget
}

func prerenderAssetSet(b Buckets, root, app string, cache *ISR) (*AssetSet, error) {
	if cache == nil {
		return nil, nil
	}

	seeds, err := prerender.Seeds(appArtifactRoot(root, app), cache.Prefix)
	if err != nil {
		return nil, err
	}

	var uploads []prerenderUpload
	manifest := newSetManifest()
	targets := map[string]uploadTarget{
		prerender.EntrySegment: entryTarget(b),
		prerender.FetchSegment: {up: b.Objects, bucket: b.Assets, tier: b.Tier},
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

	return &AssetSet{
		name:   PrerenderAssetSetName,
		app:    app,
		files:  manifest.files,
		digest: manifest.digest(),
		push: func(ctx context.Context, progress progress.Log) error {
			return pushPrerenderAssets(ctx, b, app, cache, uploads, progress)
		},
	}, nil
}

func pushPrerenderAssets(ctx context.Context, b Buckets, app string, cache *ISR, uploads []prerenderUpload, progress progress.Log) error {
	if err := seedTagSnapshot(ctx, b, cache, time.Now()); err != nil {
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
			return tracedUpload(ctx, u.to.up, u.to.bucket, u.key, objectHeaders{}.taggedFor(u.to), func() ([]byte, error) {
				return os.ReadFile(u.src)
			}, stats)
		})
	}
	err := g.Wait()
	emitUploadBatch(progress, uploadKindPrerenderAsset, stats, err, phaseStart)
	return err
}

type uploadTarget struct {
	up           payloads.ObjectStore
	bucket       string
	tier         environment.Tier
	edgeReadable bool
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

func seedTagSnapshot(ctx context.Context, b Buckets, cache *ISR, at time.Time) error {
	body, err := json.Marshal(prerender.GenesisTagSnapshot(at))
	if err != nil {
		return fmt.Errorf("encode tag snapshot: %w", err)
	}

	for _, target := range snapshotTargets(b) {
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

func snapshotTargets(b Buckets) []uploadTarget {
	target := uploadTarget{up: b.Objects, bucket: b.Assets, tier: b.Tier}
	if target.validate() != nil {
		return nil
	}
	return []uploadTarget{target}
}

func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
		return true
	}
	var respErr *awshttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusPreconditionFailed
}

func entryTarget(b Buckets) uploadTarget {
	return uploadTarget{up: b.Objects, bucket: b.Assets, tier: b.Tier, edgeReadable: b.CanEdgeRead()}
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
