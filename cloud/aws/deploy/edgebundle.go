package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const edgeBundleFile = "edge/bundle.json"

func appEdgeR2Prefix(slug, app, buildID string) string {
	return path.Join("edge", slug, sanitizeWorkerName(app), buildID)
}

func appEdgeBundleR2Key(slug, app, buildID string) string {
	return path.Join(appEdgeR2Prefix(slug, app, buildID), "bundle.json")
}

func readEdgeBundle(cfg Config, app string) ([]byte, bool, error) {
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), filepath.FromSlash(edgeBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read edge bundle for %s: %w", app, err)
	}
	return raw, true, nil
}

func uploadEdgeBundles(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest) error {
	if cfg.CacheStoreBucket == "" || cfg.CacheStoreUploader == nil {
		return nil
	}
	for _, app := range manifestApps(manifest) {
		if app.GetFramework() != frameworkNext {
			continue
		}
		name := app.GetName()
		bundle, ok, err := readEdgeBundle(cfg, name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		buildID, err := nextBuildID(cfg, name)
		if err != nil {
			return err
		}
		key := appEdgeBundleR2Key(manifest.GetSlug(), name, buildID)
		if err := putArtifact(ctx, cfg.CacheStoreUploader, cfg.CacheStoreBucket, key, "application/json", bundle); err != nil {
			return err
		}
	}
	return nil
}

func appEdgeWorkers(cfg Config, slug, app, buildID string) (*edge.Code, error) {
	loader, ok := cfg.Edge.(edge.CodeLoader)
	if !ok {
		return nil, nil
	}
	bundle, ok, err := readEdgeBundle(cfg, app)
	if err != nil || !ok {
		return nil, err
	}
	compatDate, compatFlags := loader.CodeRuntime()
	return &edge.Code{
		BundleKey:   appEdgeBundleR2Key(slug, app, buildID),
		ID:          loaderID(bundle, compatDate, compatFlags),
		CompatDate:  compatDate,
		CompatFlags: compatFlags,
	}, nil
}

func loaderID(bundle []byte, compatDate string, compatFlags []string) string {
	h := sha256.New()
	h.Write(bundle)
	h.Write([]byte(compatDate))
	h.Write([]byte(strings.Join(compatFlags, ",")))
	return hex.EncodeToString(h.Sum(nil))
}
