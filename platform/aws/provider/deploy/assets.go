package deploy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const staticAssetsDir = "static"

const (
	immutableCacheControl  = "public, max-age=31536000, immutable"
	revalidateCacheControl = "public, max-age=0, must-revalidate"
)

const (
	nextStaticSegment    = "_next/static/"
	serviceWorkerSegment = "service-worker/"
)

var assetContentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".map":         "application/json; charset=utf-8",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".eot":         "application/vnd.ms-fontobject",
	".txt":         "text/plain; charset=utf-8",
	".xml":         "application/xml",
	".webmanifest": "application/manifest+json",
	".wasm":        "application/wasm",
}

var metadataContentTypes = map[string]string{
	"robots.txt":    "text/plain",
	"manifest.json": "application/manifest+json",
}

func assetContentType(rel string) string {
	name := strings.ToLower(rel[strings.LastIndex(rel, "/")+1:])
	if ct, ok := metadataContentTypes[name]; ok {
		return ct
	}
	dot := strings.LastIndex(name, ".")
	if dot == -1 {
		return "application/octet-stream"
	}
	if ct, ok := assetContentTypes[name[dot:]]; ok {
		return ct
	}
	return "application/octet-stream"
}

func assetCacheControl(rel string) string {
	path := "/" + rel
	at := strings.Index(path, "/"+nextStaticSegment)
	if at == -1 {
		return revalidateCacheControl
	}
	item := path[at+len("/"+nextStaticSegment):]
	if strings.HasPrefix(item, serviceWorkerSegment) {
		return revalidateCacheControl
	}
	return immutableCacheControl
}

func assetHeaders(rel string) objectHeaders {
	return objectHeaders{contentType: assetContentType(rel), cacheControl: assetCacheControl(rel)}
}

const imageConfigFile = "image-config.json"

func appAssetPrefix(c naming.Coordinate) string {
	return c.AssetKey("")
}

func assetPlaneTargets(cfg Config) []uploadTarget {
	return []uploadTarget{
		{up: cfg.CacheStoreUploader, bucket: cfg.CacheStoreBucket},
		{up: cfg.Uploader, bucket: cfg.AssetBucket},
	}
}

func uploadStaticAssets(ctx context.Context, cfg Config, manifest *contractv1.Manifest, builds appBuilds) error {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil
	}

	assetBucket := uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket}
	plane := assetPlaneTargets(cfg)
	type upload struct {
		key, src string
		to       []uploadTarget
		replace  bool
		headers  objectHeaders
	}
	var uploads []upload
	for _, app := range manifestApps(manifest) {
		if app.GetFramework() != frameworkNext {
			continue
		}
		name := app.GetName()
		coord := builds.coords[name]
		root := appArtifactRoot(cfg.ArtifactRoot, name)
		dir := filepath.Join(root, staticAssetsDir)
		rels, err := collectFiles(dir)
		if err != nil {
			return err
		}
		for _, rel := range rels {
			uploads = append(uploads, upload{
				key:     coord.AssetKey(rel),
				src:     filepath.Join(dir, filepath.FromSlash(rel)),
				to:      plane,
				headers: assetHeaders(rel),
			})
		}
		imageConfig := filepath.Join(root, imageConfigFile)
		switch _, err := os.Stat(imageConfig); {
		case err == nil:
			uploads = append(uploads, upload{
				key:     coord.ImageConfigKey(),
				src:     imageConfig,
				to:      []uploadTarget{assetBucket},
				replace: true,
				headers: objectHeaders{contentType: "application/json"},
			})
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("stat image config for %s: %w", name, err)
		}
	}
	if len(uploads) == 0 {
		return nil
	}
	if err := assetBucket.validate(); err != nil {
		return err
	}

	phaseStart := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	stats := newUploadBatchStats()
	for _, u := range uploads {
		read := sync.OnceValues(func() ([]byte, error) { return os.ReadFile(u.src) })
		for _, to := range u.to {
			g.Go(func() error {
				if u.replace {
					data, err := read()
					if err != nil {
						readErr := fmt.Errorf("read %s: %w", u.src, err)
						now := time.Now()
						stats.record(uploadOutcome{Start: now, End: now, Failed: true, Err: readErr})
						return readErr
					}
					return tracedPut(ctx, to.up, to.bucket, u.key, u.headers, data, stats)
				}
				return tracedUpload(ctx, to.up, to.bucket, u.key, u.headers, read, stats)
			})
		}
	}
	err := g.Wait()
	emitUploadBatch(cfg.Tracer, cfg.Stages.Uploading.ID, uploadKindStaticAsset, stats, err, phaseStart)
	return err
}
