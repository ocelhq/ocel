package registry

import (
	"context"
	"testing"
)

func TestAnInstallsImagesLiveUnderItsOwnNamespaceAndNoOther(t *testing.T) {
	t.Parallel()

	api := &fakeECR{token: "AWS:tok3n", endpoint: "https://123456789012.dkr.ecr.us-east-1.amazonaws.com"}
	target, err := Resolve(context.Background(), api, "acme")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := target.ImageRef("shop.web", "sha256-abc"); got != "123456789012.dkr.ecr.us-east-1.amazonaws.com/acme/shop.web:sha256-abc" {
		t.Errorf("ImageRef = %q, want the repository under the namespace acme", got)
	}
	if !Owns(target, "acme") || Owns(target, "ocel") {
		t.Errorf("Owns(acme) = %t, Owns(ocel) = %t, want only the namespace the target was resolved for", Owns(target, "acme"), Owns(target, "ocel"))
	}
	if IsInRegistry(target, "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/shop.web:sha256-abc") {
		t.Error("IsInRegistry() = true for an image under namespace ocel, so acme's reclaim would delete another install's images")
	}
}

func TestARepositoryIsTaggedWithTheNamespaceThatPushedToIt(t *testing.T) {
	t.Parallel()

	api := &fakeECR{}
	if _, err := ensure(context.Background(), api, "acme", "acme/shop.web"); err != nil {
		t.Fatalf("ensure() = %v", err)
	}
	if got := tagsOf(api.createdTags["acme/shop.web"]); got["ocel:managed-by"] != "acme" || got["ocel:project"] != "shop" {
		t.Errorf("the repository was created tagged %v, want ocel:managed-by=acme and ocel:project=shop", got)
	}
}
