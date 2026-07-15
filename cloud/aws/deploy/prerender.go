package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// prerenderManifest is the subset of a Next app's routing-manifest.json the
// asset upload reads: the build id (which keys the objects, immutable per build)
// and the app name (the <app-id> key segment).
type prerenderManifest struct {
	BuildID string `json:"buildId"`
	AppName string `json:"appName"`
}

// assetPrefix is the key prefix every asset this app publishes to the
// account-global bucket sits under: <env>/<project-id>/<app-id>/<build-id>. It
// is also what the function's IAM policy is scoped to and what the cache
// handler joins its keys onto, so all three agree by construction. Returns ""
// for a manifest with no Next.js function.
func assetPrefix(cfg Config, manifest *deploymentsv1.Manifest) (string, error) {
	if !hasNextFunction(manifest.GetFunctions()) {
		return "", nil
	}
	var pm prerenderManifest
	raw, err := os.ReadFile(filepath.Join(cfg.ArtifactRoot, "routing-manifest.json"))
	if err != nil {
		return "", fmt.Errorf("read routing manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &pm); err != nil {
		return "", fmt.Errorf("parse routing manifest: %w", err)
	}
	if pm.BuildID == "" || pm.AppName == "" {
		return "", fmt.Errorf("routing manifest is missing buildId or appName; rebuild the app")
	}
	// The app-id key segment reuses the worker-name sanitizer so it agrees with
	// how the app is otherwise addressed, and stays a safe, stable path token.
	appID := sanitizeWorkerName(pm.AppName)
	return path.Join(cfg.Env, manifest.GetProjectId(), appID, pm.BuildID), nil
}

// uploadPrerenderAssets uploads a Next app's build output to the account-global
// asset bucket under assetPrefix: the Vercel-style prerender assets the adapter
// emits beside each function (*.prerender-config.json, *.prerender-fallback.*),
// and the seeded ISR cache entries under cache/. The .func directories
// (deployed Lambda trees) are skipped so the crawl never descends into their
// traced node_modules. It is a no-op for a manifest with no Next.js function.
func uploadPrerenderAssets(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) error {
	prefix, err := assetPrefix(cfg, manifest)
	if err != nil || prefix == "" {
		return err
	}

	functionsDir := filepath.Join(cfg.ArtifactRoot, "functions")
	assets, err := collectPrerenderAssets(functionsDir)
	if err != nil {
		return err
	}
	// Cache entries live beside functions/ rather than inside it, and keep their
	// cache/ segment in the key: that is exactly where the handler looks.
	cacheEntries, err := collectFiles(filepath.Join(cfg.ArtifactRoot, "cache"))
	if err != nil {
		return err
	}
	if len(assets) == 0 && len(cacheEntries) == 0 {
		return nil
	}

	if cfg.AssetBucket == "" {
		return fmt.Errorf("Next app has prerender assets but no asset bucket is configured; re-run `ocel bootstrap`")
	}
	if cfg.Uploader == nil {
		return fmt.Errorf("no asset uploader configured")
	}

	type upload struct{ key, src string }
	uploads := make([]upload, 0, len(assets)+len(cacheEntries))
	for _, rel := range assets {
		uploads = append(uploads, upload{
			key: path.Join(prefix, rel),
			src: filepath.Join(functionsDir, filepath.FromSlash(rel)),
		})
	}
	for _, rel := range cacheEntries {
		uploads = append(uploads, upload{
			key: path.Join(prefix, "cache", rel),
			src: filepath.Join(cfg.ArtifactRoot, "cache", filepath.FromSlash(rel)),
		})
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // bounded S3 conns
	for _, u := range uploads {
		g.Go(func() error {
			return uploadArtifact(ctx, cfg.Uploader, cfg.AssetBucket, u.key, func() ([]byte, error) {
				return os.ReadFile(u.src)
			})
		})
	}
	return g.Wait()
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

// collectPrerenderAssets returns every prerender config + fallback under dir as
// slash-separated paths relative to dir, skipping descent into `.func`
// directories. A missing dir yields no assets.
func collectPrerenderAssets(dir string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasSuffix(d.Name(), ".func") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".prerender-config.json") &&
			!strings.Contains(name, ".prerender-fallback.") {
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
		return nil, fmt.Errorf("crawl prerender assets %s: %w", dir, err)
	}
	return rels, nil
}
