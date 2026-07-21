package edge

import (
	"encoding/json"
	"fmt"
	"os"
)

// EnvWorkerBundles names the environment variable the npm launcher exports the
// worker bundle manifest in: a JSON object of framework -> edge -> path to that
// pairing's compiled worker entrypoint.
const EnvWorkerBundles = "OCEL_WORKER_BUNDLES"

// BundleManifest is every worker bundle the launcher shipped, keyed by the
// framework that produced it and the edge it runs on.
type BundleManifest map[Framework]map[Kind]string

// LoadBundleManifest reads the manifest the npm launcher exported.
func LoadBundleManifest() (BundleManifest, error) {
	raw := os.Getenv(EnvWorkerBundles)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set; the ocel CLI must be run through its npm launcher", EnvWorkerBundles)
	}
	var m BundleManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", EnvWorkerBundles, err)
	}
	return m, nil
}

// Path returns the bundle for a framework on an edge, erroring by naming both
// when the pairing ships none.
func (m BundleManifest) Path(f Framework, k Kind) (string, error) {
	if p := m[f][k]; p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no worker bundle for framework %q on edge %q", f, k)
}

// EnvStoreWorkerBundles names the environment variable the npm launcher
// exports the deployments-store worker's bundle manifest in (ADR 0001): a JSON
// object of edge -> path to that edge's compiled deployments-store
// entrypoint. Separate from EnvWorkerBundles because the store worker is not a
// framework's worker — it is the root stack's own, one per edge kind rather
// than one per (framework, edge) pairing.
const EnvStoreWorkerBundles = "OCEL_STORE_WORKER_BUNDLES"

// StoreBundleManifest is the deployments-store worker bundle the launcher
// shipped for each edge kind.
type StoreBundleManifest map[Kind]string

// LoadStoreBundleManifest reads the manifest the npm launcher exported.
func LoadStoreBundleManifest() (StoreBundleManifest, error) {
	raw := os.Getenv(EnvStoreWorkerBundles)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set; the ocel CLI must be run through its npm launcher", EnvStoreWorkerBundles)
	}
	var m StoreBundleManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", EnvStoreWorkerBundles, err)
	}
	return m, nil
}

// Path returns the deployments-store bundle for an edge kind, erroring by
// naming it when the edge ships none.
func (m StoreBundleManifest) Path(k Kind) (string, error) {
	if p := m[k]; p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no deployments-store worker bundle for edge %q", k)
}
