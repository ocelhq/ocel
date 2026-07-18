package edge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// The bindings the compiled Next.js worker reads. They are the contract between
// this assembly and the worker's TypeScript (readInterceptionConfig and the
// router), so both sides of it live here.
const (
	nextAssetBinding    = "ASSETS"
	nextFunctionURLsVar = "FUNCTION_URLS"

	nextAccessKeyIDVar   = "OCEL_EDGE_ACCESS_KEY_ID"
	nextSecretKeySecret  = "OCEL_EDGE_SECRET_KEY"
	nextRegionVar        = "OCEL_AWS_REGION"
	nextBucketVar        = "OCEL_ISR_BUCKET"
	nextPrefixVar        = "OCEL_ISR_PREFIX"
	nextTagTableVar      = "OCEL_STATE_TABLE"
	nextTagTableIndexVar = "OCEL_STATE_TABLE_INDEX"
	nextTagNamespaceVar  = "OCEL_ISR_TAG_NAMESPACE"
)

// nextRoutingManifest is the build output the worker dispatches from. It ships
// as a text module (no module upload has a JSON type); the worker JSON.parses
// its string default export.
const nextRoutingManifest = "routing-manifest.json"

// nextStaticDir holds the truly-static files served alongside the worker.
const nextStaticDir = "static"

// assembleNextCloudflare builds the Cloudflare Worker fronting a Next.js app:
// the compiled bundle, the routing manifest it dispatches from, its static
// assets, and the bindings it routes and reads its cache with.
func assembleNextCloudflare(src WorkerSource, r Resolver) (Worker, error) {
	main, err := os.ReadFile(src.BundlePath)
	if err != nil {
		return Worker{}, fmt.Errorf("read next worker bundle: %w", err)
	}
	routing, err := os.ReadFile(filepath.Join(src.ArtifactRoot, nextRoutingManifest))
	if err != nil {
		return Worker{}, fmt.Errorf("read routing manifest: %w", err)
	}
	assets, err := collectStaticAssets(filepath.Join(src.ArtifactRoot, nextStaticDir))
	if err != nil {
		return Worker{}, fmt.Errorf("collect static assets: %w", err)
	}

	vars, err := nextRouteVars(src.Routes, r)
	if err != nil {
		return Worker{}, err
	}
	secrets, err := nextCacheBindings(r, vars)
	if err != nil {
		return Worker{}, err
	}

	return Worker{
		Main: WorkerModule{
			Name:        "index.js",
			ContentType: "application/javascript+module",
			Content:     main,
		},
		Modules: []WorkerModule{{
			Name:        nextRoutingManifest,
			ContentType: "text/plain",
			Content:     routing,
		}},
		Vars:         vars,
		Secrets:      secrets,
		AssetBinding: nextAssetBinding,
		Assets:       assets,
	}, nil
}

// nextRouteVars builds the route-id -> Function URL map the worker dispatches
// with. Every route the app serves must resolve: a worker missing one would
// answer that route with an error rather than reaching the origin.
func nextRouteVars(routes []string, r Resolver) (map[string]string, error) {
	urls := make(map[string]string, len(routes))
	for _, route := range routes {
		url, err := r.FunctionURL(route)
		if err != nil {
			return nil, err
		}
		urls[route] = url
	}
	encoded, err := json.Marshal(urls)
	if err != nil {
		return nil, fmt.Errorf("marshal function urls: %w", err)
	}
	return map[string]string{nextFunctionURLsVar: string(encoded)}, nil
}

// nextCacheBindings adds the coordinates the worker serves ISR directly from,
// returning the signing secret as a secret binding so it never appears as
// plaintext in the upload metadata. An unconfigured cache adds nothing and is
// not an error: the worker then forwards prerender routes to the origin exactly
// as it otherwise would, so interception stays strictly additive.
func nextCacheBindings(r Resolver, vars map[string]string) (map[string]string, error) {
	store, configured, err := r.CacheStore()
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, nil
	}

	vars[nextBucketVar] = store.Bucket
	vars[nextPrefixVar] = store.Prefix
	vars[nextRegionVar] = store.Region
	vars[nextTagTableVar] = store.TagTable
	vars[nextTagTableIndexVar] = store.TagTableIndex
	vars[nextTagNamespaceVar] = store.TagNamespace
	vars[nextAccessKeyIDVar] = store.Credentials.AccessKeyID

	return map[string]string{nextSecretKeySecret: store.Credentials.SecretKey}, nil
}

// collectStaticAssets reads every file under dir into a StaticAsset carrying its
// URL path, contents, content hash, and size. A missing directory yields no
// assets. Paths use forward slashes rooted at "/" regardless of host OS.
func collectStaticAssets(dir string) ([]StaticAsset, error) {
	var assets []StaticAsset
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dir {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		urlPath := "/" + filepath.ToSlash(rel)
		ext := path.Ext(urlPath)
		if ext != "" {
			ext = ext[1:] // drop the leading dot
		}
		assets = append(assets, StaticAsset{
			Path:    urlPath,
			Content: content,
			Hash:    hashAsset(content, ext),
			Size:    int64(len(content)),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assets, nil
}

// hashAsset computes the content hash an edge's assets upload session keys a
// file by: the SHA-256 of the base64-encoded contents concatenated with the file
// extension (no leading dot), hex-encoded and truncated to 32 characters. This
// mirrors wrangler's algorithm; a mismatch would make the session reject the
// upload.
func hashAsset(content []byte, ext string) string {
	sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(content) + ext))
	return hex.EncodeToString(sum[:])[:32]
}
