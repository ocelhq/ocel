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

// prerenderManifest is the subset of a Next app's routing-manifest.json the
// asset upload reads: the build id, which keys the objects and is immutable per
// build.
type prerenderManifest struct {
	BuildID string `json:"buildId"`
}

// appAssetPrefix is the key prefix every asset one app publishes to the
// account-global bucket sits under: <env>/<project-id>/<app-id>/<build-id>. It
// is also what that app's IAM policy is scoped to and what its cache handler
// joins its keys onto, so all three agree by construction — and no two apps ever
// address the same slice.
func appAssetPrefix(cfg Config, projectID, app string) (string, error) {
	var pm prerenderManifest
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), "routing-manifest.json"))
	if err != nil {
		return "", fmt.Errorf("read routing manifest for %s: %w", app, err)
	}
	if err := json.Unmarshal(raw, &pm); err != nil {
		return "", fmt.Errorf("parse routing manifest for %s: %w", app, err)
	}
	if pm.BuildID == "" {
		return "", fmt.Errorf("routing manifest for %s is missing buildId; rebuild the app", app)
	}
	// The app-id key segment reuses the worker-name sanitizer so it agrees with
	// how the app is otherwise addressed, and stays a safe, stable path token.
	return path.Join(cfg.Env, projectID, sanitizeWorkerName(app), pm.BuildID), nil
}

// appCaches describes the ISR cache of every app in the manifest that keeps
// one, keyed by app name. An app whose framework has no server-side cache is
// absent, and so gets neither cache env nor a cache grant.
func appCaches(cfg Config, manifest *deploymentsv1.Manifest) (map[string]*isrConfig, error) {
	caches := map[string]*isrConfig{}
	for _, fn := range manifest.GetFunctions() {
		app := fn.GetApp()
		if fn.GetFramework() != frameworkNext || caches[app] != nil {
			continue
		}
		prefix, err := appAssetPrefix(cfg, manifest.GetProjectId(), app)
		if err != nil {
			return nil, err
		}
		caches[app] = &isrConfig{
			Bucket:             cfg.AssetBucket,
			Prefix:             prefix,
			Table:              cfg.StateTable,
			TableARN:           cfg.StateTableARN,
			CacheStoreParam:    cfg.CacheStoreParam,
			CacheStoreParamARN: cfg.CacheStoreParamARN,
		}
	}
	return caches, nil
}

// uploadPrerenderAssets uploads each app's seeded ISR cache entries to the
// account-global asset bucket under that app's own prefix, keeping their cache/
// segment in the key so the deployed cache handler reads them back at exactly
// that path. It is a no-op for a manifest with no cached app.
func uploadPrerenderAssets(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) error {
	caches, err := appCaches(cfg, manifest)
	if err != nil {
		return err
	}

	type upload struct{ key, src string }
	var uploads []upload
	for app, cache := range caches {
		// Cache entries live beside functions/ rather than inside it, and keep
		// their cache/ segment in the key: that is exactly where the handler looks.
		cacheDir := filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), "cache")
		entries, err := collectFiles(cacheDir)
		if err != nil {
			return err
		}
		for _, rel := range entries {
			uploads = append(uploads, upload{
				key: path.Join(cache.Prefix, "cache", rel),
				src: filepath.Join(cacheDir, filepath.FromSlash(rel)),
			})
		}
	}
	// Seeded before the entries and independently of them: an app with nothing
	// prerendered still has an edge reading its clock on every request.
	if err := seedTagSnapshots(ctx, cfg, caches, time.Now()); err != nil {
		return err
	}
	if len(uploads) == 0 {
		return nil
	}

	up, bucket := entryTarget(cfg)
	if bucket == "" {
		return fmt.Errorf("this project has cache entries to seed but no asset bucket is configured; re-run `ocel bootstrap`")
	}
	if up == nil {
		return fmt.Errorf("no asset uploader configured")
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // bounded S3 conns
	for _, u := range uploads {
		g.Go(func() error {
			return uploadArtifact(ctx, up, bucket, u.key, func() ([]byte, error) {
				return os.ReadFile(u.src)
			})
		})
	}
	return g.Wait()
}

// tagSnapshot mirrors the TypeScript TagSnapshot in @ocel/next-cache. The deploy
// writes this document once and the Lambda publisher rewrites it thereafter, so
// the two agree on the field names, the version and the validity window by way
// of the shared fixture both sides' tests read — never by way of a shared type.
type tagSnapshot struct {
	Version     int                  `json:"version"`
	DeployedAt  int64                `json:"deployedAt"`
	GeneratedAt int64                `json:"generatedAt"`
	ValidUntil  int64                `json:"validUntil"`
	Records     map[string]tagRecord `json:"records"`
}

// tagRecord is when a tag was last invalidated. The deploy never writes one; it
// is here because the document it seeds is the same document the publisher fills.
type tagRecord struct {
	Stale   int64 `json:"stale,omitempty"`
	Expired int64 `json:"expired,omitempty"`
}

const (
	tagSnapshotVersion = 1
	// The publisher's snapshotValidityMs. Duplicated across the language
	// boundary and held equal by the fixture, whose TypeScript test asserts the
	// window against the constant itself.
	snapshotValidityMs = 5 * 60 * 1000
)

// genesisSnapshot is a build's tag clock at the moment it deploys: empty, and
// anchored. Empty is the correct content rather than a placeholder — every entry
// in the build has a lastModified at or after this instant, so no invalidation
// recorded before it can apply to any of them.
func genesisSnapshot(at time.Time) tagSnapshot {
	ms := at.UnixMilli()
	return tagSnapshot{
		Version:     tagSnapshotVersion,
		DeployedAt:  ms,
		GeneratedAt: ms,
		ValidUntil:  ms + snapshotValidityMs,
		Records:     map[string]tagRecord{},
	}
}

// seedTagSnapshots writes each app's genesis snapshot beside that app's entries,
// so a fresh build intercepts from its first request instead of falling open
// until some Lambda warms and publishes.
//
// It is also the only place the build's deploy time is ever recorded. The
// publisher prunes records that can no longer expire anything in this build, and
// that proof rests entirely on deployedAt; no environment variable carries a
// build timestamp, and anything the publisher could synthesize would be an upper
// bound, which would prune records that can still legitimately expire an entry.
//
// Create-only. A redeploy of the same build must keep the snapshot the running
// build accumulated, so an object already present is the expected outcome and
// not a failure. A substrate that adopted no store has no edge replica at all:
// nothing to seed, and nothing to fail.
func seedTagSnapshots(ctx context.Context, cfg Config, caches map[string]*isrConfig, at time.Time) error {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil
	}
	body, err := json.Marshal(genesisSnapshot(at))
	if err != nil {
		return fmt.Errorf("encode tag snapshot: %w", err)
	}

	up, bucket := entryTarget(cfg)
	for _, cache := range caches {
		key := cache.Prefix + "/tag-clock.json"
		_, err := up.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(body),
			ContentType: aws.String("application/json"),
			IfNoneMatch: aws.String("*"),
		})
		if err != nil && !isPreconditionFailed(err) {
			return fmt.Errorf("seed tag snapshot %s: %w", key, err)
		}
	}
	return nil
}

// isPreconditionFailed reports whether a conditional write lost to the object it
// conditioned on. Nothing else may be read as one: a denied grant must still
// surface as a failed deploy rather than as a snapshot silently never seeded.
func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
		return true
	}
	var respErr *awshttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusPreconditionFailed
}

// entryTarget is where seeded ISR cache entries land: the substrate's adopted
// cache store when its edge offered one, and the provider's own asset bucket
// when it did not. The cache handler makes the same choice from the coordinates
// the membrane injects, so the two agree on one bucket by construction.
func entryTarget(cfg Config) (ArtifactUploader, string) {
	if cfg.CacheStoreBucket != "" && cfg.CacheStoreUploader != nil {
		return cfg.CacheStoreUploader, cfg.CacheStoreBucket
	}
	return cfg.Uploader, cfg.AssetBucket
}

// collectFiles returns every file under dir as slash-separated paths relative to
// it. A missing dir yields no files — an app with nothing prerendered emits no
// cache entries.
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
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("crawl %s: %w", dir, err)
	}
	return rels, nil
}
