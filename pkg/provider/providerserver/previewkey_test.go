package providerserver_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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

	previewKey := seal.Bind("preview key")
	if opened := vendor.Cipher().(*fake.Cipher).OpenedUnder(previewKey); opened > 1 {
		t.Errorf("the preview key was opened %d times in one provider session, want at most once", opened)
	}
}

type keylessCipher struct{ seal.Cipher }

func (keylessCipher) Seal(context.Context, environment.Tier, seal.AssociatedData, []byte) ([]byte, error) {
	return nil, provider.WantedPartError{Part: provider.VariablesKeyPart, Err: refusal.Refuse(refusal.CodeNotReady, "the preview bootstrap has no key to seal a value under")}
}

type keylessProvider struct{ *fake.Provider }

func (p keylessProvider) Cipher() seal.Cipher { return keylessCipher{p.Provider.Cipher()} }

func TestEnsurePreviewAliasRefusedForWantOfAKeyRecordsTheKeyAsWantedForTheNextBootstrap(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{})
	client := servedBy(t, keylessProvider{vendor})

	_, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       "abcdefghijklmnop",
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
	})
	if err == nil {
		t.Fatal("EnsurePreviewAlias() with no key to seal the preview key under succeeded, want it refused")
	}
	wants, err := stackrecords.ListBootstrapWants(context.Background(), vendor.KeyValues(), environment.TierPreview)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(wants[provider.VariablesKeyPart], []string{"shop"}) {
		t.Errorf("preview bootstrap wants = %v, want shop to want the variables key, for `ocel bootstrap preview` to add it", wants)
	}
}
