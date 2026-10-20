package registry

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestAnInstallsImagesLiveUnderItsOwnNamespaceAndNoOther(t *testing.T) {
	t.Parallel()

	api := &fakeECR{token: "AWS:tok3n", endpoint: "https://123456789012.dkr.ecr.us-east-1.amazonaws.com"}
	target, err := Resolve(context.Background(), api, "acme", environment.TierProduction)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := target.ImageRef("shop.web", "sha256-abc"); got != "123456789012.dkr.ecr.us-east-1.amazonaws.com/acme/prod/shop.web:sha256-abc" {
		t.Errorf("ImageRef = %q, want the repository under the namespace acme", got)
	}
	if !Owns(target, "acme") || Owns(target, "ocel") {
		t.Errorf("Owns(acme) = %t, Owns(ocel) = %t, want only the namespace the target was resolved for", Owns(target, "acme"), Owns(target, "ocel"))
	}
	if IsInRegistry(target, "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/prod/shop.web:sha256-abc") {
		t.Error("IsInRegistry() = true for an image under namespace ocel, so acme's reclaim would delete another install's images")
	}
}

func TestEachTiersImagesLiveInRepositoriesOfThatTierAlone(t *testing.T) {
	t.Parallel()

	api := &fakeECR{token: "AWS:tok3n", endpoint: "https://123456789012.dkr.ecr.us-east-1.amazonaws.com"}
	production, err := Resolve(context.Background(), api, "ocel", environment.TierProduction)
	if err != nil {
		t.Fatalf("Resolve production: %v", err)
	}
	preview, err := Resolve(context.Background(), api, "ocel", environment.TierPreview)
	if err != nil {
		t.Fatalf("Resolve preview: %v", err)
	}
	productionRef, previewRef := production.ImageRef("shop.web", "sha256-abc"), preview.ImageRef("shop.web", "sha256-abc")
	if productionRef != "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/prod/shop.web:sha256-abc" {
		t.Errorf("production ImageRef = %q, want it under ocel/prod", productionRef)
	}
	if previewRef != "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/preview/shop.web:sha256-abc" {
		t.Errorf("preview ImageRef = %q, want it under ocel/preview", previewRef)
	}
	if IsInRegistry(preview, productionRef) || IsInRegistry(production, previewRef) {
		t.Error("IsInRegistry() = true for an image of the other tier, so one tier's reclaim would delete the other's images")
	}
	if !Owns(production, "ocel") || !Owns(preview, "ocel") {
		t.Error("Owns() = false for a target Resolve minted for a tier, so its pushes would never create the repository")
	}
}

func TestARepositoryIsTaggedWithTheNamespaceThatPushedToIt(t *testing.T) {
	t.Parallel()

	api := &fakeECR{}
	target := anECRTarget()
	target.Namespace = "acme/prod"
	if _, err := Images(target, api, "acme").(ecrImages).ensure(context.Background(), "acme/prod/shop.web"); err != nil {
		t.Fatalf("ensure() = %v", err)
	}
	if got := tagsOf(api.createdTags["acme/prod/shop.web"]); got["ocel:managed-by"] != "acme" || got["ocel:project"] != "shop" {
		t.Errorf("the repository was created tagged %v, want ocel:managed-by=acme and ocel:project=shop", got)
	}
}

func TestARepositoryOutsideItsTiersRepositoriesIsNeverCreated(t *testing.T) {
	t.Parallel()

	api := &fakeECR{}
	if _, err := anECRStore(api).ensure(context.Background(), "ocel/preview/shop.web"); err == nil || len(api.created) != 0 {
		t.Errorf("ensure() = %v, created %v, want a refusal: the production tier's push has no repository of the preview tier to create", err, api.created)
	}
}
