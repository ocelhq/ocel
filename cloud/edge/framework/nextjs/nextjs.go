// Package nextjs assembles the edge worker fronting a Next.js app. It is one
// framework's entry in the registry: it knows Next.js build output and the
// bindings its own worker code reads, and nothing about any particular edge
// beyond which one it registers for.
package nextjs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cloud/edge"
)

// The bindings the compiled Next.js worker reads. They are the contract between
// this assembly and the worker's TypeScript (the router and its signer), so
// both sides of it live here. The worker resolves an app's Function URLs and
// ISR coordinates from its active Deployment record (ADR 0002) and its cache
// store from the OCEL_CACHE_STORE object-store binding, so the only bindings it
// still reads here are the edge credentials it signs its Function-URL forwards
// with.
const (
	assetBinding       = "ASSETS"
	objectStoreBinding = "OCEL_CACHE_STORE"
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

	if err := validateRoutes(src.Routes, r); err != nil {
		return edge.Worker{}, err
	}
	vars, secrets := signingBindings(r)

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
		// The worker always asks for its cache store; an edge with none to bind
		// simply binds nothing and the worker falls back to the origin.
		ObjectStore: edge.ObjectStore{Binding: objectStoreBinding},
	}, nil
}

// validateRoutes fails the deploy if any route the app serves cannot be
// resolved to a Function URL, so a worker is never assembled that would answer
// a route with an error rather than reaching its origin. The URLs themselves
// travel to the worker in its active Deployment record (ADR 0002), not as a
// binding.
func validateRoutes(routes []string, r edge.Resolver) error {
	for _, route := range routes {
		if _, err := r.FunctionURL(route); err != nil {
			return err
		}
	}
	return nil
}

// signingBindings injects the edge reader's IAM credentials the worker signs
// its Function-URL forwards with (the app's Lambdas require AWS_IAM auth). The
// secret is returned as a secret binding so it never appears as plaintext in
// the upload metadata. No credentials — a substrate whose bootstrap predates
// them — adds nothing and is not an error: the worker then forwards unsigned,
// which only reaches a Lambda that is still public.
func signingBindings(r edge.Resolver) (vars, secrets map[string]string) {
	creds, ok := r.EdgeCredentials()
	if !ok {
		return nil, nil
	}
	return map[string]string{edge.EdgeAccessKeyIDVar: creds.AccessKeyID},
		map[string]string{edge.EdgeSecretKeyVar: creds.SecretKey}
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
