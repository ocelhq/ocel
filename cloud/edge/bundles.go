package edge

import (
	"encoding/json"
	"fmt"
	"os"
)

const EnvWorkerBundles = "OCEL_WORKER_BUNDLES"

type BundleManifest map[Framework]map[Kind]string

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

func (m BundleManifest) Path(f Framework, k Kind) (string, error) {
	if p := m[f][k]; p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no worker bundle for framework %q on edge %q", f, k)
}

const (
	EnvStoreWorkerBundles     = "OCEL_STORE_WORKER_BUNDLES"
	EnvISRWriterWorkerBundles = "OCEL_ISR_WRITER_WORKER_BUNDLES"
)

type StoreBundleManifest map[Kind]string

func LoadStoreBundleManifest() (StoreBundleManifest, error) {
	return loadKindBundles(EnvStoreWorkerBundles)
}

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

func (m StoreBundleManifest) Path(k Kind) (string, error) {
	if p := m[k]; p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no account-level worker bundle for edge %q", k)
}
