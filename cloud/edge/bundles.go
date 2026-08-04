package edge

import (
	"encoding/json"
	"fmt"
	"os"
)

// EnvWorkerBundles names the environment variable the CLI exports the worker
// bundle manifest in: a JSON object of framework -> edge -> path to that
// pairing's compiled worker entrypoint, resolved from the project's
// materialized platform dist. The provider binary is a separate process, so
// env is how it learns those paths.
const EnvWorkerBundles = "OCEL_WORKER_BUNDLES"

// BundleManifest is every worker bundle the CLI shipped, keyed by the
// framework that produced it and the edge it runs on.
type BundleManifest map[Framework]map[Kind]string

// LoadBundleManifest reads the manifest the CLI exported.
func LoadBundleManifest() (BundleManifest, error) {
	raw := os.Getenv(EnvWorkerBundles)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set; the ocel CLI exports it when spawning a provider", EnvWorkerBundles)
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

// EnvStoreWorkerBundles / EnvISRWriterWorkerBundles name the environment
// variables the CLI exports the account-level workers' bundle manifests in
// (ADR 0001): a JSON object of edge -> path to that edge's compiled entrypoint.
// Separate from EnvWorkerBundles because neither is a framework's worker — each
// is one per edge kind rather than one per (framework, edge) pairing.
const (
	EnvStoreWorkerBundles     = "OCEL_STORE_WORKER_BUNDLES"
	EnvISRWriterWorkerBundles = "OCEL_ISR_WRITER_WORKER_BUNDLES"
)

// StoreBundleManifest is one account-level worker's bundle for each edge kind.
type StoreBundleManifest map[Kind]string

// LoadStoreBundleManifest reads the deployments-store manifest the CLI
// exported.
func LoadStoreBundleManifest() (StoreBundleManifest, error) {
	return loadKindBundles(EnvStoreWorkerBundles)
}

// LoadISRWriterBundleManifest reads the ISR writer manifest the CLI exported.
func LoadISRWriterBundleManifest() (StoreBundleManifest, error) {
	return loadKindBundles(EnvISRWriterWorkerBundles)
}

func loadKindBundles(envName string) (StoreBundleManifest, error) {
	raw := os.Getenv(envName)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set; the ocel CLI exports it when spawning a provider", envName)
	}
	var m StoreBundleManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", envName, err)
	}
	return m, nil
}

// Path returns the account-level worker bundle for an edge kind, erroring by
// naming it when the edge ships none.
func (m StoreBundleManifest) Path(k Kind) (string, error) {
	if p := m[k]; p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no account-level worker bundle for edge %q", k)
}
