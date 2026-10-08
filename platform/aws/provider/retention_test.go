package aws

import (
	"context"
	"maps"
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

func shopStacks(t *testing.T) (keyvalue.Store, provider.StackRef) {
	t.Helper()
	store := fake.NewKeyValues()
	own := naming.AppStack("prod", "web", naming.NewReleaseToken("b2", ""))
	previous := naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))
	preview := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b3", ""))
	recordImage(t, store, environment.TierProduction, "shop", own, "ecr/ocel/shop.web:sha256-own")
	recordImage(t, store, environment.TierProduction, "shop", previous, "ecr/ocel/shop.web:sha256-previous")
	recordImage(t, store, environment.TierPreview, "shop", preview, "ecr/ocel/shop.web:sha256-preview")
	recordImage(t, store, environment.TierProduction, "blog", previous, "ecr/ocel/blog.web:sha256-other")
	return store, provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: own}
}

func TestAReconcileKeepsTheImageItsOwnStackStillRecords(t *testing.T) {
	t.Parallel()

	store, own := shopStacks(t)

	kept, err := reconciledKeptImages(context.Background(), store, own)
	if err != nil {
		t.Fatalf("reconciledKeptImages() = %v", err)
	}

	if !kept["ecr/ocel/shop.web:sha256-own"] {
		t.Errorf("reconciledKeptImages() = %v, want the image the reconciled stack records: a release that failed leaves its record naming the image the service still runs", kept)
	}
}

func TestTheImagesAReconcileKeepsAreWhatEveryStackOfTheProjectRecords(t *testing.T) {
	t.Parallel()

	store, own := shopStacks(t)

	kept, err := reconciledKeptImages(context.Background(), store, own)
	if err != nil {
		t.Fatalf("reconciledKeptImages() = %v", err)
	}

	want := map[string]bool{"ecr/ocel/shop.web:sha256-own": true, "ecr/ocel/shop.web:sha256-previous": true, "ecr/ocel/shop.web:sha256-preview": true}
	if !maps.Equal(kept, want) {
		t.Errorf("reconciledKeptImages() = %v, want %v: another project's stacks are not listed at all", kept, want)
	}
}

func TestTheImagesAForgetKeepsAreWhatEveryOtherStackOfTheProjectRecords(t *testing.T) {
	t.Parallel()

	store, own := shopStacks(t)

	kept, err := forgottenKeptImages(context.Background(), store, own)
	if err != nil {
		t.Fatalf("forgottenKeptImages() = %v", err)
	}

	want := map[string]bool{"ecr/ocel/shop.web:sha256-previous": true, "ecr/ocel/shop.web:sha256-preview": true}
	if !maps.Equal(kept, want) {
		t.Errorf("forgottenKeptImages() = %v, want %v: the stack being destroyed runs nothing once it is gone", kept, want)
	}
}

func TestAProjectNothingRecordsKeepsNoImage(t *testing.T) {
	t.Parallel()

	kept, err := reconciledKeptImages(context.Background(), fake.NewKeyValues(), provider.StackRef{
		Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", naming.NewReleaseToken("b1", "")),
	})
	if err != nil || len(kept) != 0 {
		t.Errorf("reconciledKeptImages() = %v, %v, want none", kept, err)
	}
}
