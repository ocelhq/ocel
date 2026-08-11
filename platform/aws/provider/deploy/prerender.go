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
	"path"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type prerenderManifest struct {
	BuildID string `json:"buildId"`
}

func appAssetPrefixFor(env, slug, app, buildID string) string {
	return path.Join(env, slug, sanitizeWorkerName(app), buildID)
}

type appBuilds struct {
	ids    map[string]string
	caches map[string]*isrConfig
}

func resolveAppBuilds(cfg Config, manifest *deploymentsv1.Manifest) (appBuilds, error) {
	builds := appBuilds{ids: map[string]string{}, caches: map[string]*isrConfig{}}
	for _, fn := range manifest.GetFunctions() {
		app := fn.GetApp()
		if fn.GetFramework() != frameworkNext || builds.caches[app] != nil {
			continue
		}
		buildID, err := builds.resolve(cfg, app)
		if err != nil {
			return appBuilds{}, err
		}
		prefix := appAssetPrefixFor(cfg.Env, manifest.GetSlug(), app, buildID)
		cache := &isrConfig{
			Bucket:   cfg.AssetBucket,
			Prefix:   prefix,
			Table:    cfg.StateTable,
			TableARN: cfg.StateTableARN,
		}
		if isrEntriesAdopted(cfg) {
			cache.CacheStoreBucket = cfg.CacheStoreBucket
			cache.WriterURL = cfg.ISRWriterEndpoint + "/" + prefix + "/entry"
			cache.WriterSecret = isrWriteSecret(cfg.ISRWriterSeed, prefix)
		}
		builds.caches[app] = cache
	}
	for _, app := range manifestApps(manifest) {
		if app.GetFramework() != frameworkNext {
			continue
		}
		if _, err := builds.resolve(cfg, app.GetName()); err != nil {
			return appBuilds{}, err
		}
	}
	return builds, nil
}

func (b appBuilds) resolve(cfg Config, app string) (string, error) {
	if id := b.ids[app]; id != "" {
		return id, nil
	}
	id, err := nextBuildID(cfg, app)
	if err != nil {
		return "", err
	}
	b.ids[app] = id
	return id, nil
}

func uploadPrerenderAssets(ctx context.Context, cfg Config, builds appBuilds) error {
	caches := builds.caches

	segments := []struct {
		dir string
		to  uploadTarget
	}{
		{"cache", entryTarget(cfg)},
		{"fetch-cache", uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket}},
	}

	type upload struct {
		key, src string
		to       uploadTarget
	}
	var uploads []upload
	for app, cache := range caches {
		for _, seg := range segments {
			dir := filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), seg.dir)
			entries, err := collectFiles(dir)
			if err != nil {
				return err
			}
			for _, rel := range entries {
				uploads = append(uploads, upload{
					key: path.Join(cache.Prefix, seg.dir, rel),
					src: filepath.Join(dir, filepath.FromSlash(rel)),
					to:  seg.to,
				})
			}
		}
	}
	if err := seedTagSnapshots(ctx, cfg, caches, time.Now()); err != nil {
		return err
	}
	if err := seedISRWriters(ctx, cfg, caches); err != nil {
		return err
	}
	if len(uploads) == 0 {
		return nil
	}

	for _, seg := range segments {
		if err := seg.to.validate(); err != nil {
			return err
		}
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	for _, u := range uploads {
		g.Go(func() error {
			return uploadArtifact(ctx, u.to.up, u.to.bucket, u.key, "", func() ([]byte, error) {
				return os.ReadFile(u.src)
			})
		})
	}
	return g.Wait()
}

type uploadTarget struct {
	up     ArtifactUploader
	bucket string
}

func (t uploadTarget) validate() error {
	if t.bucket == "" {
		return fmt.Errorf("this project has objects to publish but no asset bucket is configured; re-run `ocel bootstrap`")
	}
	if t.up == nil {
		return fmt.Errorf("no asset uploader configured")
	}
	return nil
}

type tagSnapshot struct {
	Version     int                  `json:"version"`
	DeployedAt  int64                `json:"deployedAt"`
	GeneratedAt int64                `json:"generatedAt"`
	Records     map[string]tagRecord `json:"records"`
}

type tagRecord struct {
	Stale   int64 `json:"stale,omitempty"`
	Expired int64 `json:"expired,omitempty"`
}

const (
	tagSnapshotVersion = 1
	tagSnapshotSuffix  = "/tag-clock.json"
)

func genesisSnapshot(at time.Time) tagSnapshot {
	ms := at.UnixMilli()
	return tagSnapshot{
		Version:     tagSnapshotVersion,
		DeployedAt:  ms,
		GeneratedAt: ms,
		Records:     map[string]tagRecord{},
	}
}

func seedTagSnapshots(ctx context.Context, cfg Config, caches map[string]*isrConfig, at time.Time) error {
	body, err := json.Marshal(genesisSnapshot(at))
	if err != nil {
		return fmt.Errorf("encode tag snapshot: %w", err)
	}

	for _, target := range snapshotTargets(cfg) {
		for _, cache := range caches {
			key := cache.Prefix + tagSnapshotSuffix
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
	}
	return nil
}

func snapshotTargets(cfg Config) []uploadTarget {
	var targets []uploadTarget
	for _, t := range []uploadTarget{{up: cfg.Uploader, bucket: cfg.AssetBucket}, entryTarget(cfg)} {
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
	if isrEntriesAdopted(cfg) {
		return uploadTarget{up: cfg.CacheStoreUploader, bucket: cfg.CacheStoreBucket}
	}
	return uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket}
}

func isrEntriesAdopted(cfg Config) bool {
	return cfg.CacheStoreBucket != "" && cfg.CacheStoreUploader != nil
}

func collectFiles(dir string) ([]string, error) {
	var rels []string
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
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("crawl %s: %w", dir, err)
	}
	return rels, nil
}
