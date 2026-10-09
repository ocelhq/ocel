package providerserver_test

import (
	"context"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/seal"
)

func TestAProviderSessionOpensThePreviewKeyAtMostOnce(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)

	for range 2 {
		if _, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
			Slug:        "shop",
			Environment: previewRequest().GetEnvironment(),
			Token:       "abcdefghijklmnop",
			Apps:        []string{"web"},
			Domains:     []string{"*.preview.example"},
		}); err != nil {
			t.Fatalf("EnsurePreviewAlias: %v", err)
		}
		if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
			t.Fatalf("Deploy() = %q", result.GetError())
		}
	}

	previewKey := seal.AssociatedData{{Name: "record", Value: "preview-key"}}
	if opened := vendor.Cipher().(*fake.Cipher).OpenedUnder(previewKey); opened > 1 {
		t.Errorf("the preview key was opened %d times in one provider session, want at most once", opened)
	}
}
