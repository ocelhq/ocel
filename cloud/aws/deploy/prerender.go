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

// uploadPrerenderAssets crawls a Next app's functions output for the Vercel-
// style prerender assets the adapter emits (*.prerender-config.json and
// *.prerender-fallback.*) and uploads each to the account-global asset bucket,
// keyed by <env>/<project-id>/<app-id>/<build-id>/<relpath> where relpath is
// the file's path relative to the functions directory. The .func directories
// (deployed Lambda trees) are skipped so the crawl never descends into their
// traced node_modules. It is a no-op for a manifest with no Next.js function.
func uploadPrerenderAssets(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) error {
	if !hasNextFunction(manifest.GetFunctions()) {
		return nil
	}

	functionsDir := filepath.Join(cfg.ArtifactRoot, "functions")
	assets, err := collectPrerenderAssets(functionsDir)
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return nil
	}

	if cfg.AssetBucket == "" {
		return fmt.Errorf("Next app has prerender assets but no asset bucket is configured; re-run `ocel bootstrap`")
	}
	if cfg.Uploader == nil {
		return fmt.Errorf("no asset uploader configured")
	}

	var pm prerenderManifest
	raw, err := os.ReadFile(filepath.Join(cfg.ArtifactRoot, "routing-manifest.json"))
	if err != nil {
		return fmt.Errorf("read routing manifest for prerender upload: %w", err)
	}
	if err := json.Unmarshal(raw, &pm); err != nil {
		return fmt.Errorf("parse routing manifest for prerender upload: %w", err)
	}
	if pm.BuildID == "" || pm.AppName == "" {
		return fmt.Errorf("routing manifest is missing buildId or appName; rebuild the app")
	}

	// The app-id key segment reuses the worker-name sanitizer so it agrees with
	// how the app is otherwise addressed, and stays a safe, stable path token.
	appID := sanitizeWorkerName(pm.AppName)
	prefix := path.Join(cfg.Env, manifest.GetProjectId(), appID, pm.BuildID)

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // bounded S3 conns
	for _, rel := range assets {
		g.Go(func() error {
			key := path.Join(prefix, rel)
			src := filepath.Join(functionsDir, filepath.FromSlash(rel))
			return uploadArtifact(ctx, cfg.Uploader, cfg.AssetBucket, key, func() ([]byte, error) {
				return os.ReadFile(src)
			})
		})
	}
	return g.Wait()
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
