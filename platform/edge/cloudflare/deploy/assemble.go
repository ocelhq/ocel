package cloudflare

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	assetBinding       = "ASSETS"
	objectStoreBinding = "OCEL_CACHE_STORE"
)

func (p *provider) AssembleApp(src edge.WorkerSource, r edge.Resolver) (edge.Worker, error) {
	main, err := os.ReadFile(src.BundlePath)
	if err != nil {
		return edge.Worker{}, fmt.Errorf("read edge worker bundle: %w", err)
	}
	routing, err := os.ReadFile(filepath.Join(src.ArtifactRoot, edge.RoutingManifestFile))
	if err != nil {
		return edge.Worker{}, fmt.Errorf("read routing manifest: %w", err)
	}
	assets, err := collectStaticAssets(filepath.Join(src.ArtifactRoot, edge.StaticAssetDir))
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
			Name:        edge.RoutingManifestFile,
			ContentType: "text/plain",
			Content:     routing,
		}},
		Vars:         vars,
		Secrets:      secrets,
		AssetBinding: assetBinding,
		Assets:       assets,
		ObjectStore:  edge.ObjectStore{Binding: objectStoreBinding},
	}, nil
}

func validateRoutes(routes []string, r edge.Resolver) error {
	for _, route := range routes {
		if _, err := r.FunctionURL(route); err != nil {
			return err
		}
	}
	return nil
}

func signingBindings(r edge.Resolver) (vars, secrets map[string]string) {
	creds, ok := r.EdgeCredentials()
	if !ok {
		return nil, nil
	}
	return map[string]string{edge.EdgeAccessKeyIDVar: creds.AccessKeyID},
		map[string]string{edge.EdgeSecretKeyVar: creds.SecretKey}
}

func collectStaticAssets(dir string) ([]edge.StaticAsset, error) {
	var assets []edge.StaticAsset
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == dir {
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
