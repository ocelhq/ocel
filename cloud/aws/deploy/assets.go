package deploy

import (
	"context"
	"os"
	"path"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// staticAssetsDir holds the truly-static files a Next.js app's build emits,
// mirroring cloud/edge/framework/nextjs's staticDir — the same directory the
// preview path's Workers Assets upload still reads.
const staticAssetsDir = "static"

// appAssetR2Prefix is the R2 key prefix (ADR 0002) a build's static assets
// upload under: assets/<project>/<app>/<build-id>, disjoint from the isr
// cache-entry prefix (its own env/project/app/build-id root under a
// different top segment) so the two lifecycles never collide. The frozen
// worker reads this back verbatim as the Deployment record's AssetPrefix and
// joins it with a request's pathname to form the object key.
func appAssetR2Prefix(projectID, app, buildID string) string {
	return path.Join("assets", projectID, sanitizeWorkerName(app), buildID)
}

// uploadStaticAssets uploads every Next.js app's static/ build output to the
// account-global R2 cache store, under that build's own assets/<project>/
// <app>/<build-id> prefix (ADR 0002) — replacing the old per-script Workers
// Assets binding, which cannot survive the frozen generic worker sharing one
// script across every rollback-able build.
//
// A substrate whose edge offered no cache store uploads nothing: the frozen
// worker then has nowhere to read assets from and static routes simply 404,
// the same posture an unadopted ISR store leaves prerendering in.
func uploadStaticAssets(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) error {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil
	}

	type upload struct{ key, src string }
	var uploads []upload
	for _, app := range manifestApps(manifest) {
		if app.GetFramework() != frameworkNext {
			continue
		}
		name := app.GetName()
		buildID, err := nextBuildID(cfg, name)
		if err != nil {
			return err
		}
		dir := filepath.Join(appArtifactRoot(cfg.ArtifactRoot, name), staticAssetsDir)
		rels, err := collectFiles(dir)
		if err != nil {
			return err
		}
		prefix := appAssetR2Prefix(manifest.GetProjectId(), name, buildID)
		for _, rel := range rels {
			uploads = append(uploads, upload{
				key: path.Join(prefix, rel),
				src: filepath.Join(dir, filepath.FromSlash(rel)),
			})
		}
	}
	if len(uploads) == 0 {
		return nil
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(8) // bounded R2/S3 conns, same budget uploadPrerenderAssets uses
	for _, u := range uploads {
		g.Go(func() error {
			return uploadArtifact(ctx, cfg.CacheStoreUploader, cfg.CacheStoreBucket, u.key, func() ([]byte, error) {
				return os.ReadFile(u.src)
			})
		})
	}
	return g.Wait()
}
