package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestACloudRunServiceOfATierCarriesItsTiersTagOnceHoweverOftenItIsReleased(t *testing.T) {
	ctx := context.Background()
	p := providerServing(&googletest.Run{}, t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(p.Clients)
	s := serves("ocel-shop-prod-app")
	s.Tier = environment.TierProduction

	for _, image := range []string{"europe-west1-docker.pkg.dev/acme/ocel/app@sha256:one", "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"} {
		s.Image = image
		if _, err := p.Services().DeployService(ctx, s, nil); err != nil {
			t.Fatal(err)
		}
	}
	service := p.Clients.RunServiceTagged("ocel-shop-prod-app")
	if got := tags.Bound(service.Name); !slices.Equal(got, []string{tags.ValueOf(environment.TierProduction)}) {
		t.Errorf("the released service carries %v, want the production value once", got)
	}
}

func TestACloudRunServiceFoundWithoutItsTiersTagIsGivenItByTheNextRelease(t *testing.T) {
	ctx := context.Background()
	p := providerServing(&googletest.Run{}, t)
	s := serves("ocel-shop-pr-7-app")
	s.Tier = environment.TierPreview
	if _, err := p.Services().DeployService(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	tags := googletest.NewTieredTags(t)
	tags.Tagged(p.Clients)

	s.Image = "europe-west1-docker.pkg.dev/acme/ocel/app@sha256:two"
	if _, err := p.Services().DeployService(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	if got := tags.Bound(p.Clients.RunServiceTagged("ocel-shop-pr-7-app").Name); !slices.Equal(got, []string{tags.ValueOf(environment.TierPreview)}) {
		t.Errorf("the service released untagged and then again carries %v, want the preview value", got)
	}
}

func TestACloudRunServiceBothTiersShareCarriesNoTierTag(t *testing.T) {
	ctx := context.Background()
	p := providerServing(&googletest.Run{}, t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(p.Clients)

	shared := serves("ocel-connector")
	shared.Tier, shared.Shared = "", true

	if _, err := p.Services().DeployService(ctx, shared, nil); err != nil {
		t.Fatal(err)
	}
	if len(tags.BindingsMade) != 0 {
		t.Errorf("a service no tier owns was bound to %v, and either tier's value fences it from the other", tags.BindingsMade)
	}
}

func TestACloudRunServiceOfNoTierThatNothingSharesIsRefusedRatherThanReleasedUntagged(t *testing.T) {
	ctx := context.Background()
	server := &googletest.Run{}
	p := providerServing(server, t)
	s := serves("ocel-shop-prod-app")
	s.Tier = ""

	if _, err := p.Services().DeployService(ctx, s, nil); err == nil {
		t.Errorf("a service of no tier was released, want a refusal: it would carry no tier tag, and the deploy credential is fenced to its tier by that tag")
	}
}
