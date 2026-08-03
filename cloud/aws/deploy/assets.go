package deploy

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sync"

	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// staticAssetsDir holds the truly-static files a Next.js app's build emits,
// mirroring cloud/edge/framework/nextjs's staticDir — the same directory the
// preview path's Workers Assets upload still reads.
const staticAssetsDir = "static"

// imageConfigFile is the compiled image configuration the Next adapter emits
// beside routing-manifest.json. It is absent for an app that generates no
// /_next/image URLs (a custom loader, or unoptimized images).
const imageConfigFile = "image-config.json"

// appAssetR2Prefix is the R2 key prefix (ADR 0002) a build's static assets
// upload under: assets/<project>/<app>/<build-id>, disjoint from the isr
// cache-entry prefix (its own env/project/app/build-id root under a
// different top segment) so the two lifecycles never collide. The frozen
// worker reads this back verbatim as the Deployment record's AssetPrefix and
// joins it with a request's pathname to form the object key.
func appAssetR2Prefix(slug, app, buildID string) string {
	return path.Join("assets", slug, sanitizeWorkerName(app), buildID)
}

// imageConfigKey is where a build's compiled image config publishes, for the
// account-global image optimizer to load out of the asset bucket. It sits
// outside the assets/ prefix on purpose: that prefix is the app's public web
// root — the frozen worker serves any unmatched path out of it — so a config
// under it would be served to the internet, and would collide byte-for-byte
// with a project's own public/image-config.json.
func imageConfigKey(slug, app, buildID string) string {
	return path.Join("image-config", slug, sanitizeWorkerName(app), buildID+".json")
}

// assetPlaneTargets is where a build's static assets publish, under identical
// keys in both: the adopted R2 cache store the frozen worker reads on every
// request, and the account's own S3 asset bucket the image optimizer reads —
// so that optimizer never needs R2 credentials.
//
// The ISR cache is deliberately not mirrored this way (see entryTarget): the
// optimizer never reads it, so a second copy would double the upload time and
// the storage for no reader.
func assetPlaneTargets(cfg Config) []uploadTarget {
	return []uploadTarget{
		{up: cfg.CacheStoreUploader, bucket: cfg.CacheStoreBucket},
		{up: cfg.Uploader, bucket: cfg.AssetBucket},
	}
}

// uploadStaticAssets uploads every Next.js app's static/ build output to both
// halves of the asset plane under that build's own assets/<project>/<app>/
// <build-id> prefix (ADR 0002) — replacing the old per-script Workers Assets
// binding, which cannot survive the frozen generic worker sharing one script
// across every rollback-able build — plus the build's compiled image config, to
// the asset bucket alone under imageConfigKey. Either target failing fails the
// deploy.
//
// A substrate whose edge offered no cache store uploads nothing: the frozen
// worker then has nowhere to read assets from and static routes simply 404,
// the same posture an unadopted ISR store leaves prerendering in.
func uploadStaticAssets(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) error {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil
	}

	assetBucket := uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket}
	plane := assetPlaneTargets(cfg)
	type upload struct {
		key, src string
		to       []uploadTarget
		// replace puts unconditionally rather than skip-if-exists. The image
		// config needs it: the manifest's configHash covers these exact bytes and
		// the optimizer refuses a file that does not hash to it, so an object left
		// over from a same-build-id publish of a different config would break every
		// image request. Content-keyed assets carry no such risk.
		replace bool
	}
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
		root := appArtifactRoot(cfg.ArtifactRoot, name)
		dir := filepath.Join(root, staticAssetsDir)
		rels, err := collectFiles(dir)
		if err != nil {
			return err
		}
		prefix := appAssetR2Prefix(manifest.GetSlug(), name, buildID)
		for _, rel := range rels {
			uploads = append(uploads, upload{
				key: path.Join(prefix, rel),
				src: filepath.Join(dir, filepath.FromSlash(rel)),
				to:  plane,
			})
		}
		imageConfig := filepath.Join(root, imageConfigFile)
		switch _, err := os.Stat(imageConfig); {
		case err == nil:
			uploads = append(uploads, upload{
				key:     imageConfigKey(manifest.GetSlug(), name, buildID),
				src:     imageConfig,
				to:      []uploadTarget{assetBucket},
				replace: true,
			})
		case !os.IsNotExist(err):
			return fmt.Errorf("stat image config for %s: %w", name, err)
		}
	}
	if len(uploads) == 0 {
		return nil
	}
	if err := assetBucket.validate(); err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	for _, u := range uploads {
		// One read serves every target this object publishes to, and stays behind
		// the skip-if-exists check that may make it unnecessary.
		read := sync.OnceValues(func() ([]byte, error) { return os.ReadFile(u.src) })
		for _, to := range u.to {
			g.Go(func() error {
				// An extension mime can't resolve stays "" so the worker's own
				// fallback decides, rather than a clobbering octet-stream here.
				ct := mime.TypeByExtension(path.Ext(u.key))
				if u.replace {
					data, err := read()
					if err != nil {
						return fmt.Errorf("read %s: %w", u.src, err)
					}
					return putArtifact(ctx, to.up, to.bucket, u.key, ct, data)
				}
				return uploadArtifact(ctx, to.up, to.bucket, u.key, ct, read)
			})
		}
	}
	return g.Wait()
}
