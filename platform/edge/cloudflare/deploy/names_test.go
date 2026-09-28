package cloudflare

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	sharedStoreScriptName  = "ocel-deployments-store"
	previewStoreScriptName = "ocel-deployments-store-preview"

	isrWriterScriptName        = "ocel-isr-writer"
	previewISRWriterScriptName = "ocel-isr-writer-preview"

	cacheStoreBucketName        = "ocel-edge-cache"
	previewCacheStoreBucketName = "ocel-edge-cache-preview"
)

func cacheStoreName(tier environment.Tier) string {
	name, err := cacheStoreNameFor("ocel", tier)
	if err != nil {
		panic(err)
	}
	return name
}

func TestAccountNames(t *testing.T) {
	t.Parallel()

	derivations := []struct {
		name          string
		nameFor       func(string, environment.Tier) (string, error)
		prod, preview string
	}{
		{"the deployments store", storeScriptNameFor, sharedStoreScriptName, previewStoreScriptName},
		{"the isr writer", isrWriterScriptNameFor, isrWriterScriptName, previewISRWriterScriptName},
		{"the edge cache store", cacheStoreNameFor, cacheStoreBucketName, previewCacheStoreBucketName},
	}

	for _, tc := range derivations {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			t.Run("the default namespace keeps the names it always had", func(t *testing.T) {
				t.Parallel()

				prod, err := tc.nameFor("ocel", environment.TierProduction)
				if err != nil {
					t.Fatalf("production: %v", err)
				}
				preview, err := tc.nameFor("ocel", environment.TierPreview)
				if err != nil {
					t.Fatalf("preview: %v", err)
				}
				if prod != tc.prod || preview != tc.preview {
					t.Errorf("names = (%q, %q), want (%q, %q)", prod, preview, tc.prod, tc.preview)
				}
			})

			t.Run("another namespace names its own", func(t *testing.T) {
				t.Parallel()

				prod, err := tc.nameFor("j-1874-deploy-next", environment.TierProduction)
				if err != nil {
					t.Fatalf("production: %v", err)
				}
				preview, err := tc.nameFor("j-1874-deploy-next", environment.TierPreview)
				if err != nil {
					t.Fatalf("preview: %v", err)
				}
				if !strings.HasPrefix(prod, "j-1874-deploy-next-") || !strings.HasPrefix(preview, "j-1874-deploy-next-") {
					t.Errorf("names = (%q, %q), want both under j-1874-deploy-next-", prod, preview)
				}
				if prod == tc.prod || preview == tc.preview || prod == preview {
					t.Errorf("names = (%q, %q) collide with the default namespace or each other", prod, preview)
				}
			})

			t.Run("no namespace is an error", func(t *testing.T) {
				t.Parallel()

				if _, err := tc.nameFor("", environment.TierProduction); err == nil {
					t.Error("nameFor(no namespace) = nil error, want an error")
				}
			})

			t.Run("an unknown tier is an error", func(t *testing.T) {
				t.Parallel()

				if _, err := tc.nameFor("ocel", environment.Tier("nonsense")); err == nil {
					t.Error("nameFor(unknown tier) = nil error, want an error")
				}
			})

			t.Run("a name longer than Cloudflare allows is refused", func(t *testing.T) {
				t.Parallel()

				long := strings.Repeat("a", longestAccountName)
				if _, err := tc.nameFor(long, environment.TierPreview); err == nil || !strings.Contains(err.Error(), long) {
					t.Errorf("nameFor(%d-character namespace) err = %v, want one naming it", len(long), err)
				}
			})
		})
	}

	t.Run("the isr writer and the deployments store never share a name", func(t *testing.T) {
		t.Parallel()

		if isrWriterScriptName == sharedStoreScriptName || previewISRWriterScriptName == previewStoreScriptName {
			t.Error("the isr-writer and deployments-store scripts must be distinct")
		}
	})
}
