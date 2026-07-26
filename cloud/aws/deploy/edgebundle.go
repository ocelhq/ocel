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

// edgeBundleFile is the adapter's edge bundle inside an app's build output: the
// shim, chunks and entry table backing every edge route and the middleware. A
// build with no edge output at all writes none, which is the common case.
const edgeBundleFile = "edge/bundle.json"

// appEdgeR2Prefix is the R2 key prefix (ADR 0002) a build's edge bundle uploads
// under: edge/<project>/<app>/<build-id>. Build-scoped like the static-asset
// prefix rather than content-addressed, so a prune of one build reclaims the
// whole prefix without ever touching a bundle a live deployment still loads.
// Its own top segment keeps it out of reach of the worker's static-asset serve,
// which the bundle's inlined server env (Next's preview-mode and server-action
// keys) must never be exposed through.
func appEdgeR2Prefix(projectID, app, buildID string) string {
	return path.Join("edge", projectID, sanitizeWorkerName(app), buildID)
}

// appEdgeBundleR2Key is the object the frozen worker's loader callback GETs,
// carried verbatim as the Deployment record's EdgeWorkers.BundleKey.
func appEdgeBundleR2Key(projectID, app, buildID string) string {
	return path.Join(appEdgeR2Prefix(projectID, app, buildID), "bundle.json")
}

// readEdgeBundle reads one app's edge bundle, reporting ok=false when the build
// produced none — an app with no middleware and no edge route, not an error.
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

// uploadEdgeBundles uploads every Next.js app's edge bundle to the
// account-global R2 cache store, under that build's own edge/<project>/<app>/
// <build-id> prefix — the same publish-then-point-at-it shape uploadStaticAssets
// uses, and what lets a Deployment carry its own edge code without any script
// upload (ADR 0002: rollback stays a pointer flip).
//
// A substrate whose edge offered no cache store uploads nothing, the same
// posture uploadStaticAssets takes: the frozen worker then has nowhere to load
// a bundle from and edge routes fail closed.
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
		key := appEdgeBundleR2Key(manifest.GetProjectId(), name, buildID)
		// Overwritten, never skipped-if-exists: the key is build-scoped while
		// the loader id is content-addressed, so an app whose generateBuildId
		// is constant would keep the old bytes under the key and have the
		// loader cache them under the new bundle's id — silently serving the
		// previous build's edge code. Static assets can skip because Next
		// content-hashes their filenames; this key carries no hash at all.
		if err := putArtifact(ctx, cfg.CacheStoreUploader, cfg.CacheStoreBucket, key, "application/json", bundle); err != nil {
			return err
		}
	}
	return nil
}

// appEdgeWorkers is the Deployment record's EdgeWorkers slot for one app's
// build: where uploadEdgeBundles published the bundle, and the id its code is
// loaded under. It reports nil when the build produced no bundle, and equally
// when the edge cannot load code at all — that edge would have nothing to do
// with the slot.
func appEdgeWorkers(cfg Config, projectID, app, buildID string) (*edge.Code, error) {
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
		BundleKey:   appEdgeBundleR2Key(projectID, app, buildID),
		ID:          loaderID(bundle, compatDate, compatFlags),
		CompatDate:  compatDate,
		CompatFlags: compatFlags,
	}, nil
}

// loaderID keys a bundle's code in the edge's loader cache, whose contract is
// same id, same code. It folds the runtime settings in alongside the bytes
// because the loader evaluates the code under them: reusing an id across a
// compat bump would leave warm isolates on the old runtime and cold ones on the
// new, serving the same deployment two different ways.
func loaderID(bundle []byte, compatDate string, compatFlags []string) string {
	h := sha256.New()
	h.Write(bundle)
	h.Write([]byte(compatDate))
	h.Write([]byte(strings.Join(compatFlags, ",")))
	return hex.EncodeToString(h.Sum(nil))
}
