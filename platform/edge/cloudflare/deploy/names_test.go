package cloudflare

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	sharedStoreScriptName  = "ocel-prod-releases-store"
	previewStoreScriptName = "ocel-preview-releases-store"
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

			t.Run("the default namespace names the account-wide workers and buckets under its tier word", func(t *testing.T) {
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
				field := naming.NamespaceField("j-1874-deploy-next")
				if !strings.HasPrefix(prod, field+"-prod-") || !strings.HasPrefix(preview, field+"-preview-") {
					t.Errorf("names = (%q, %q), want them under %s- and the tier word", prod, preview, field)
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

			t.Run("the longest namespace is named by its field within Cloudflare's limit", func(t *testing.T) {
				t.Parallel()

				long := strings.Repeat("a", provider.MaxNamespaceLength)
				name, err := tc.nameFor(long, environment.TierPreview)
				if err != nil {
					t.Fatal(err)
				}
				if len(name) > maxWorkerNameLength || !strings.HasPrefix(name, naming.NamespaceField(long)+"-preview-") {
					t.Errorf("nameFor(%d-character namespace) = %q, want it under the namespace field within %d characters", len(long), name, maxWorkerNameLength)
				}
			})
		})
	}
}
