// Package nextjs assembles the edge worker fronting a Next.js app. It is one
// framework's entry in the registry: it knows Next.js build output and the
// bindings its own worker code reads, and nothing about any particular edge
// beyond which one it registers for.
package nextjs

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cloud/edge"
)

// The bindings the compiled Next.js worker reads. They are the contract between
// this assembly and the worker's TypeScript (readInterceptionConfig and the
// router), so both sides of it live here.
const (
	assetBinding    = "ASSETS"
	functionURLsVar = "FUNCTION_URLS"

	accessKeyIDVar   = "OCEL_EDGE_ACCESS_KEY_ID"
	secretKeySecret  = "OCEL_EDGE_SECRET_KEY"
	regionVar        = "OCEL_AWS_REGION"
	bucketVar        = "OCEL_ISR_BUCKET"
	prefixVar        = "OCEL_ISR_PREFIX"
	tagTableVar      = "OCEL_STATE_TABLE"
	tagTableIndexVar = "OCEL_STATE_TABLE_INDEX"
	tagNamespaceVar  = "OCEL_ISR_TAG_NAMESPACE"
)

// routingManifest is the build output the worker dispatches from. It ships as a
// text module (no module upload has a JSON type); the worker JSON.parses its
// string default export.
const routingManifest = "routing-manifest.json"

// staticDir holds the truly-static files served alongside the worker.
const staticDir = "static"

// AssembleCloudflare builds the Cloudflare Worker fronting a Next.js app: the
// compiled bundle, the routing manifest it dispatches from, its static assets,
// and the bindings it routes and reads its cache with.
func AssembleCloudflare(src edge.WorkerSource, r edge.Resolver) (edge.Worker, error) {
	main, err := os.ReadFile(src.BundlePath)
	if err != nil {
		return edge.Worker{}, fmt.Errorf("read next worker bundle: %w", err)
	}
	routing, err := os.ReadFile(filepath.Join(src.ArtifactRoot, routingManifest))
	if err != nil {
		return edge.Worker{}, fmt.Errorf("read routing manifest: %w", err)
	}
	assets, err := collectStaticAssets(filepath.Join(src.ArtifactRoot, staticDir))
	if err != nil {
		return edge.Worker{}, fmt.Errorf("collect static assets: %w", err)
	}

	vars, err := routeVars(src.Routes, r)
	if err != nil {
		return edge.Worker{}, err
	}
	secrets, err := cacheBindings(r, vars)
	if err != nil {
		return edge.Worker{}, err
	}

	return edge.Worker{
		Main: edge.WorkerModule{
			Name:        "index.js",
			ContentType: "application/javascript+module",
			Content:     main,
		},
		Modules: []edge.WorkerModule{{
			Name:        routingManifest,
			ContentType: "text/plain",
			Content:     routing,
		}},
		Vars:         vars,
		Secrets:      secrets,
		AssetBinding: assetBinding,
		Assets:       assets,
	}, nil
}

// routeVars builds the route-id -> Function URL map the worker dispatches with.
// Every route the app serves must resolve: a worker missing one would answer
// that route with an error rather than reaching the origin.
func routeVars(routes []string, r edge.Resolver) (map[string]string, error) {
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
	return map[string]string{functionURLsVar: string(encoded)}, nil
}

// cacheBindings adds the coordinates the worker serves ISR directly from,
// returning the signing secret as a secret binding so it never appears as
// plaintext in the upload metadata. An unconfigured cache adds nothing and is
// not an error: the worker then forwards prerender routes to the origin exactly
// as it otherwise would, so interception stays strictly additive.
func cacheBindings(r edge.Resolver, vars map[string]string) (map[string]string, error) {
	store, configured, err := r.CacheStore()
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, nil
	}

	vars[bucketVar] = store.Bucket
	vars[prefixVar] = store.Prefix
	vars[regionVar] = store.Region
	vars[tagTableVar] = store.TagTable
	vars[tagTableIndexVar] = store.TagTableIndex
	vars[tagNamespaceVar] = store.TagNamespace
	vars[accessKeyIDVar] = store.Credentials.AccessKeyID

	return map[string]string{secretKeySecret: store.Credentials.SecretKey}, nil
}

// collectStaticAssets reads every file under dir into a StaticAsset carrying its
// URL path and contents. A missing directory yields no assets. Paths use forward
// slashes rooted at "/" regardless of host OS.
func collectStaticAssets(dir string) ([]edge.StaticAsset, error) {
	var assets []edge.StaticAsset
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
		assets = append(assets, edge.StaticAsset{
			Path:    "/" + filepath.ToSlash(rel),
			Content: content,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assets, nil
}
