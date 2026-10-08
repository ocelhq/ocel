package aws

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func recordImage(t *testing.T, store keyvalue.Store, tier environment.Tier, project string, name naming.StackName, images ...string) {
	t.Helper()
	recorded := stackrecords.Stack{Kind: provider.StackApp, App: name.App}
	for _, image := range images {
		recorded.Containers = append(recorded.Containers, provider.AppContainer{Name: name.App, Image: image})
	}
	if err := stackrecords.Write(context.Background(), store, tier, project, name, recorded); err != nil {
		t.Fatal(err)
	}
}

func TestTheStandingImagesAreWhatEveryOtherStackOfTheProjectRuns(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	own := naming.AppStack("prod", "web", naming.NewReleaseToken("b2", ""))
	previous := naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))
	preview := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b3", ""))
	recordImage(t, store, environment.TierProduction, "shop", own, "ecr/ocel/shop.web:sha256-own")
	recordImage(t, store, environment.TierProduction, "shop", previous, "ecr/ocel/shop.web:sha256-previous")
	recordImage(t, store, environment.TierPreview, "shop", preview, "ecr/ocel/shop.web:sha256-preview")
	recordImage(t, store, environment.TierProduction, "blog", previous, "ecr/ocel/blog.web:sha256-other")

	standing, err := standingImages(context.Background(), store, provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: own})
	if err != nil {
		t.Fatalf("standingImages() = %v", err)
	}

	want := map[string]bool{"ecr/ocel/shop.web:sha256-previous": true, "ecr/ocel/shop.web:sha256-preview": true}
	if len(standing) != len(want) {
		t.Fatalf("standingImages() = %v, want %v: the stack being reconciled is not standing, a preview and an earlier release are, and another project's stacks are not listed at all", standing, want)
	}
	for image := range want {
		if !standing[image] {
			t.Errorf("standingImages() = %v, missing %s", standing, image)
		}
	}
}

func TestTheStandingImagesOfAProjectNothingRecordsAreNone(t *testing.T) {
	t.Parallel()

	standing, err := standingImages(context.Background(), fake.NewKeyValues(), provider.StackRef{
		Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", naming.NewReleaseToken("b1", "")),
	})
	if err != nil || len(standing) != 0 {
		t.Errorf("standingImages() = %v, %v, want none", standing, err)
	}
}
