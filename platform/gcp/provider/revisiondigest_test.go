package gcp

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestAReleaseWhoseRevisionCloudRunReportsByDigestIsTheRevisionItMade(t *testing.T) {
	server := &googletest.Run{ReportsDigests: true}
	tagged := serves("ocel-shop-prod-app")
	tagged.Image = shopWebImage + "one"

	if _, revision := released(t, server, tagged); revision == "" {
		t.Error("the release reports no revision, want the one it created: Cloud Run names the image a revision runs by its digest, not the tag the release pushed")
	}
}

func TestARevisionAnotherReleaseMadeUnderThisOneIsRefused(t *testing.T) {
	server := &googletest.Run{ReportsDigests: true}
	ours := serves("ocel-shop-prod-app")
	ours.Image = shopWebImage + "one"
	theirs := ours
	theirs.Image = shopWebImage + "two"
	server.ReleasedUnder = desiredOf(t, theirs).Template

	_, err := providerServing(server, t).Services().DeployService(context.Background(), ours, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("DeployService() whose latest revision another release made = %v, want a %s refusal: this release would route traffic to the other release's image", err, refusal.CodeBusy)
	}
}

func TestPruningARevisionCloudRunReportsByDigestUntagsTheTagItsReleaseRan(t *testing.T) {
	server := &googletest.Run{ReportsDigests: true}
	p := providerServing(server, t)
	released := newReleasedStacks(p)
	first := functionRelease("d1", shopWebImage+"one")
	one := released.provision(t, first)
	active := released.provision(t, functionRelease("d2", shopWebImage+"two"))
	if _, err := p.Services().Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	if _, err := p.RemoveFunctionRevisions(context.Background(), first.Ref, []provider.Function{one}, nil, nil); err != nil {
		t.Fatalf("RemoveFunctionRevisions = %v", err)
	}

	if got, want := server.Untags(), []string{shopWebPackage + "/tags/sha256-one"}; !slices.Equal(got, want) {
		t.Errorf("pruning untagged %v, want %v: the repository keeps every image a retired revision ran otherwise", got, want)
	}
}

func TestDestroyingAServiceCloudRunReportsByDigestUntagsTheTagItsReleaseRan(t *testing.T) {
	server := &googletest.Run{ReportsDigests: true}
	p := providerServing(server, t)
	released := newReleasedStacks(p)
	only := functionRelease("d1", shopWebImage+"one")
	released.provision(t, only)

	released.destroy(t, only.Ref)

	if got, want := slices.Compact(slices.Sorted(slices.Values(server.Untags()))), []string{shopWebPackage + "/tags/sha256-one"}; !slices.Equal(got, want) {
		t.Errorf("destroying the function untagged %v, want %v: the repository keeps every image a destroyed service ran otherwise", got, want)
	}
}

func TestPruningARevisionCloudRunReportsByDigestLeavesAnImageADeployInFlightRecords(t *testing.T) {
	server := &googletest.Run{ReportsDigests: true}
	p := providerServing(server, t)
	released := newReleasedStacks(p)
	image := fake.RegistryServer + "/acme/shop.web-checkout:sha256-one"
	first := functionRelease("d1", image)
	one := released.provision(t, first)
	active := released.provision(t, functionRelease("d2", fake.RegistryServer+"/acme/shop.web-checkout:sha256-two"))
	if _, err := p.Services().Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}
	recordStartingImage(t, p, aPreviewOfTheSameApp(), image)
	pushed := fake.NewImages()

	if _, err := p.RemoveFunctionRevisions(context.Background(), first.Ref, []provider.Function{one}, pushed, nil); err != nil {
		t.Fatalf("RemoveFunctionRevisions = %v", err)
	}

	if got := pushed.Removed(); len(got) != 0 {
		t.Errorf("pruning removed %v, want nothing: a deploy in flight records %s, and the registry would lose the image it is about to run", got, image)
	}
}

func TestAReleaseWhoseRevisionCarriesNoAnnotationIsTheRevisionItMadeWhenItRunsTheTag(t *testing.T) {
	server := &googletest.Run{StripsRevisionMetadata: true}
	tagged := serves("ocel-shop-prod-app")
	tagged.Image = shopWebImage + "one"

	if _, revision := released(t, server, tagged); revision == "" {
		t.Error("the release reports no revision, want the one it created: a revision without the image annotation still names the tag it runs")
	}
}

func TestARevisionWithoutAnnotationThatRunsAnotherImageIsRefused(t *testing.T) {
	server := &googletest.Run{StripsRevisionMetadata: true}
	ours := serves("ocel-shop-prod-app")
	ours.Image = shopWebImage + "one"
	theirs := ours
	theirs.Image = shopWebImage + "two"
	server.ReleasedUnder = desiredOf(t, theirs).Template

	_, err := providerServing(server, t).Services().DeployService(context.Background(), ours, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("DeployService() whose latest revision another release made = %v, want a %s refusal: this release would route traffic to the other release's image", err, refusal.CodeBusy)
	}
}

func TestPruningARevisionWithoutAnnotationUntagsTheTagItRan(t *testing.T) {
	server := &googletest.Run{StripsRevisionMetadata: true}
	p := providerServing(server, t)
	released := newReleasedStacks(p)
	first := functionRelease("d1", shopWebImage+"one")
	one := released.provision(t, first)
	active := released.provision(t, functionRelease("d2", shopWebImage+"two"))
	if _, err := p.Services().Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	if _, err := p.RemoveFunctionRevisions(context.Background(), first.Ref, []provider.Function{one}, nil, nil); err != nil {
		t.Fatalf("RemoveFunctionRevisions = %v", err)
	}

	if got, want := server.Untags(), []string{shopWebPackage + "/tags/sha256-one"}; !slices.Equal(got, want) {
		t.Errorf("pruning untagged %v, want %v: the repository keeps every image a retired revision ran otherwise", got, want)
	}
}

func TestDestroyingAServiceWhoseRevisionsCarryNoAnnotationUntagsTheTagTheyRan(t *testing.T) {
	server := &googletest.Run{StripsRevisionMetadata: true}
	p := providerServing(server, t)
	released := newReleasedStacks(p)
	only := functionRelease("d1", shopWebImage+"one")
	released.provision(t, only)

	released.destroy(t, only.Ref)

	if got, want := slices.Compact(slices.Sorted(slices.Values(server.Untags()))), []string{shopWebPackage + "/tags/sha256-one"}; !slices.Equal(got, want) {
		t.Errorf("destroying the function untagged %v, want %v: the repository keeps every image a destroyed service ran otherwise", got, want)
	}
}
