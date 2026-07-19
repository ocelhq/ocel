package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

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
