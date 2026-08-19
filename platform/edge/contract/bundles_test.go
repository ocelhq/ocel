package edge

import (
	"strings"
	"testing"
)

func TestBundleManifest(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		path string
		load func() (KindBundleManifest, error)
	}{
		{"the app worker manifest", EnvWorkerBundles, "/pkg/entry/index.js", LoadBundleManifest},
		{"the account-level deployments-store manifest", EnvStoreWorkerBundles, "/pkg/worker-deployments-store/index.js", LoadStoreBundleManifest},
		{"the account-level isr-writer manifest", EnvISRWriterWorkerBundles, "/pkg/worker-isr-writer/index.js", LoadISRWriterBundleManifest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("loads and resolves a bundle path by edge", func(t *testing.T) {
				t.Setenv(tc.env, `{"sample":"`+tc.path+`"}`)

				m, err := tc.load()
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				got, err := m.Path(sampleKind)
				if err != nil {
					t.Fatalf("Path: %v", err)
				}
				if got != tc.path {
					t.Errorf("Path = %q, want %q", got, tc.path)
				}
			})

			t.Run("an edge with no bundle is an error that names the edge", func(t *testing.T) {
				t.Setenv(tc.env, `{"sample":"`+tc.path+`"}`)

				m, err := tc.load()
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				if _, err := m.Path("provider-native"); err == nil {
					t.Fatal("expected an error for an edge with no bundle")
				} else if !strings.Contains(err.Error(), "provider-native") {
					t.Errorf("error must name the edge, got %q", err)
				}
			})

			t.Run("an unset env is an error", func(t *testing.T) {
				t.Setenv(tc.env, "")

				if _, err := tc.load(); err == nil {
					t.Fatal("expected an error when the launcher exported no manifest")
				}
			})
		})
	}
}
