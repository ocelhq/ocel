package cloudflare

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	sharedStoreScriptName  = "ocel-releases-store"
	previewStoreScriptName = "ocel-releases-store-preview"
)

func TestAccountNames(t *testing.T) {
	t.Parallel()

	derivations := []struct {
		name          string
		nameFor       func(string, environment.Tier) (string, error)
		prod, preview string
	}{
		{"the releases store", storeScriptNameFor, sharedStoreScriptName, previewStoreScriptName},
	}

	for _, tc := range derivations {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			t.Run("the default namespace names the account-wide workers and buckets plainly", func(t *testing.T) {
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

				long := strings.Repeat("a", maxWorkerNameLength)
				if _, err := tc.nameFor(long, environment.TierPreview); err == nil || !strings.Contains(err.Error(), long) {
					t.Errorf("nameFor(%d-character namespace) err = %v, want one naming it", len(long), err)
				}
			})
		})
	}
}
