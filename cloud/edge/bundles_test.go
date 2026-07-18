package edge

import (
	"strings"
	"testing"
)

func TestBundleManifest_LoadsAndResolvesByFrameworkAndEdge(t *testing.T) {
	t.Setenv(EnvWorkerBundles, `{"next":{"cloudflare":"/pkg/worker-nextjs/index.js"}}`)

	m, err := LoadBundleManifest()
	if err != nil {
		t.Fatalf("LoadBundleManifest: %v", err)
	}
	got, err := m.Path(FrameworkNext, KindCloudflare)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if got != "/pkg/worker-nextjs/index.js" {
		t.Errorf("Path = %q", got)
	}

	_, err = m.Path(FrameworkNext, "provider-native")
	if err == nil {
		t.Fatal("expected an error for a pairing with no bundle")
	}
	if !strings.Contains(err.Error(), "next") || !strings.Contains(err.Error(), "provider-native") {
		t.Errorf("error must name both framework and edge, got %q", err)
	}
}

func TestLoadBundleManifest_UnsetEnvIsAnError(t *testing.T) {
	t.Setenv(EnvWorkerBundles, "")
	if _, err := LoadBundleManifest(); err == nil {
		t.Fatal("expected an error when the launcher exported no manifest")
	}
}
