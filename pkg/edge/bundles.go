package edge

import (
	"encoding/json"
	"fmt"
	"os"
)

const EnvWorkerBundles = "OCEL_WORKER_BUNDLES"

func LoadBundleManifest() (KindBundleManifest, error) {
	return loadKindBundles(EnvWorkerBundles)
}

const (
	EnvStoreWorkerBundles     = "OCEL_STORE_WORKER_BUNDLES"
	EnvISRWriterWorkerBundles = "OCEL_ISR_WRITER_WORKER_BUNDLES"
)

type KindBundleManifest map[Kind]string

func LoadStoreBundleManifest() (KindBundleManifest, error) {
	return loadKindBundles(EnvStoreWorkerBundles)
}

func LoadISRWriterBundleManifest() (KindBundleManifest, error) {
	return loadKindBundles(EnvISRWriterWorkerBundles)
}

func loadKindBundles(envName string) (KindBundleManifest, error) {
	raw := os.Getenv(envName)
	if raw == "" {
		return nil, fmt.Errorf("%s is not set; the ocel CLI exports it when spawning a provider", envName)
	}
	var m KindBundleManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", envName, err)
	}
	return m, nil
}

func (m KindBundleManifest) Path(k Kind) (string, error) {
	if p := m[k]; p != "" {
		return p, nil
	}
	return "", fmt.Errorf("no worker bundle for edge %q", k)
}
