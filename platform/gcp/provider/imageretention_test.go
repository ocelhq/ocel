package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestAnImageTagIsNamedInTheRepositoryItWasPushedTo(t *testing.T) {
	for image, want := range map[string]string{
		"europe-west1-docker.pkg.dev/acme/ocel-preview/web:sha256-one": "projects/acme/locations/europe-west1/repositories/ocel-preview/packages/web/tags/sha256-one",
		"europe-west1-docker.pkg.dev/acme/ocel-preview/shop/web:abc":   "projects/acme/locations/europe-west1/repositories/ocel-preview/packages/shop%2Fweb/tags/abc",
		"europe-west1-docker.pkg.dev/acme/ocel-preview/web@sha256:one": "",
		"ghcr.io/acme/web:latest":                                      "",
		"europe-west1-docker.pkg.dev/acme/web:latest":                  "",
	} {
		got, tagged := artifactTag(image)
		if got != want || tagged != (want != "") {
			t.Errorf("artifactTag(%s) = %q, %v, want %q", image, got, tagged, want)
		}
	}
}

func TestAReleaseWhoseImageAPruneUntaggedMidwayTagsItAgainAndReleasesAgain(t *testing.T) {
	tag := "projects/acme/locations/europe-west1/repositories/ocel/packages/app/tags/sha256-one"
	for name, deployed := range map[string]bool{"a brand-new service": false, "a deployed service": true} {
		t.Run(name, func(t *testing.T) {
			server := &runServer{}
			app := serves("ocel-shop-prod-app")
			app.image = "europe-west1-docker.pkg.dev/acme/ocel/app:sha256-one"
			var before string
			if deployed {
				prior := app
				prior.image = "europe-west1-docker.pkg.dev/acme/ocel/app:sha256-zero"
				_, before = released(t, server, prior)
			}
			server.untagAtRelease = tag

			_, revision := released(t, server, app)

			if got := server.retags(); len(got) != 1 || !strings.HasPrefix(got[0], tag+" -> ") {
				t.Errorf("the release tagged %v, want %s tagged again once Cloud Run said the image was gone", got, tag)
			}
			if revision == "" || revision == before {
				t.Errorf("the release reports revision %q, want a new one running %s: the retry must create a revision rather than report the one served before", revision, app.image)
			}
		})
	}
}

func TestAReleaseWhoseImageIsStillMissingAfterTaggingAgainFails(t *testing.T) {
	server := &runServer{untagAtRelease: "projects/acme/locations/europe-west1/repositories/ocel/packages/app/tags/sha256-one", untagEveryRelease: true}
	app := serves("ocel-shop-prod-app")
	app.image = "europe-west1-docker.pkg.dev/acme/ocel/app:sha256-one"

	_, err := server.open(t).deployService(t.Context(), app, nil)

	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("deployService() = %v, want Cloud Run's answer that the image is gone: the release retries once, never in a loop", err)
	}
}

const wrappedPackage = "projects/acme/locations/europe-west1/repositories/ocel/packages/app"

var wrappedTag = "sha256-" + strings.Repeat("c", 64) + "-ocel-0123456789ab"

func TestAWrappedImageAPruneUntaggedMidwayIsTaggedAgainAtTheVersionItsTagNamed(t *testing.T) {
	tag := wrappedPackage + "/tags/" + wrappedTag
	pushed := wrappedPackage + "/versions/sha256:" + strings.Repeat("d", 64)
	server := &runServer{untagAtRelease: tag, versions: map[string]string{tag: pushed}}
	app := serves("ocel-shop-prod-app")
	app.image = "europe-west1-docker.pkg.dev/acme/ocel/app:" + wrappedTag

	released(t, server, app)

	if got, want := server.retags(), []string{tag + " -> " + pushed}; !slices.Equal(got, want) {
		t.Errorf("the release tagged %v, want %v: a wrapped image's tag names the content it was built from, never the version the registry holds", got, want)
	}
}

func TestAWrappedImageWhoseTagIsGoneBeforeTheReleaseIsRefusedRatherThanTaggedAtAVersionItNeverHad(t *testing.T) {
	server := &runServer{missing: []string{wrappedPackage + "/tags/" + wrappedTag}}
	app := serves("ocel-shop-prod-app")
	app.image = "europe-west1-docker.pkg.dev/acme/ocel/app:" + wrappedTag

	_, err := server.open(t).deployService(t.Context(), app, nil)

	if err == nil || !strings.Contains(err.Error(), app.image) {
		t.Errorf("deployService() = %v, want a refusal naming %s", err, app.image)
	}
	if got := server.retags(); len(got) != 0 {
		t.Errorf("the release tagged %v, want nothing: the tag names no version the registry holds", got)
	}
}

const (
	shopWebPackage = "projects/acme/locations/europe-west1/repositories/ocel/packages/shop.web"
	shopWebImage   = "europe-west1-docker.pkg.dev/acme/ocel/shop.web:sha256-"
)

func aContainerStack() provider.StackRef {
	return provider.StackRef{
		Project: "shop",
		Tier:    environment.TierProduction,
		Name:    naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("d1", "f1")),
	}
}

func TestAReconcileLeavesATagAnotherDeployPushedBeforeItsRevisionExists(t *testing.T) {
	server := &runServer{tagged: map[string][]string{shopWebPackage: {"sha256-just-pushed", "sha256-new"}}}
	app := serves("ocel-shop-prod-web")
	app.image = shopWebImage + "new"
	released(t, server, app)

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", app.image, nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := server.untags(); len(got) != 0 {
		t.Errorf("the reconcile untagged %v, want nothing: a preview's image no revision runs yet is one its deploy is about to release, and a wrapped image's tag cannot be put back", got)
	}
}

func TestAReconcileOfAReleaseThatNeverStartedUntagsTheImageItPushed(t *testing.T) {
	server := &runServer{}

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", shopWebImage+"new", nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got, want := server.untags(), []string{shopWebPackage + "/tags/sha256-new"}; !slices.Equal(got, want) {
		t.Errorf("the reconcile untagged %v, want %v: a release that failed before it created a revision leaves its image tagged forever otherwise", got, want)
	}
}

func TestAReconcileKeepsATagAServiceElsewhereInTheRegionRuns(t *testing.T) {
	server := &runServer{
		elsewhere: map[string]string{"ocel-shop-preview-web-00001": shopWebImage + "old"},
	}

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", shopWebImage+"old", nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := server.untags(); len(got) != 0 {
		t.Errorf("the reconcile untagged %v, want nothing: another environment's service still runs that image", got)
	}
}

func TestAReconcileOfAnImageOutsideArtifactRegistryTouchesNothing(t *testing.T) {
	server := &runServer{}

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", "ghcr.io/acme/shop.web:sha256-new", nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := server.untags(); len(got) != 0 {
		t.Errorf("the reconcile untagged %v, want nothing: the project's own registry is not this provider's to prune", got)
	}
}

const projectRegistryImage = fake.RegistryServer + "/acme/shop.web:sha256-old"

func TestAReconcileRemovesAnImageFromTheProjectsRegistryThatNoRevisionRuns(t *testing.T) {
	server := &runServer{}
	pushed := fake.NewImages()

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", projectRegistryImage, pushed, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got, want := pushed.Removed(), []string{projectRegistryImage}; !slices.Equal(got, want) {
		t.Errorf("the reconcile removed %v, want %v: a destroyed stack's image stays in the project's registry forever otherwise", got, want)
	}
}

func TestAReconcileKeepsAnImageInTheProjectsRegistryARevisionRuns(t *testing.T) {
	server := &runServer{elsewhere: map[string]string{"ocel-shop-preview-web-00001": projectRegistryImage}}
	pushed := fake.NewImages()

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", projectRegistryImage, pushed, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := pushed.Removed(); len(got) != 0 {
		t.Errorf("the reconcile removed %v, want nothing: another environment's service still runs that image", got)
	}
}

func TestDestroyingAContainerUntagsTheImageItRan(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	app := serves("ocel-shop-prod-web")
	app.image = shopWebImage + "new"
	ref := aContainerStack()
	releasedOn(t, p, app)
	if err := stackrecords.Write(context.Background(), released.store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		Containers: []provider.AppContainer{{Name: "web", Physical: app.service, Image: app.image}},
	}); err != nil {
		t.Fatal(err)
	}

	released.destroy(t, ref)

	want := []string{shopWebPackage + "/tags/sha256-new"}
	got := slices.Compact(slices.Sorted(slices.Values(server.untags())))
	if !slices.Equal(got, want) {
		t.Errorf("destroying the container untagged %v, want %v: the repository keeps every tagged image a destroyed project ran otherwise", got, want)
	}
}

func releasedOn(t *testing.T, p *Provider, s serving) {
	t.Helper()
	if _, err := p.deployService(context.Background(), s, nil); err != nil {
		t.Fatalf("deployService(%s) = %v", s.service, err)
	}
}

func recordStartingImage(t *testing.T, p *Provider, ref provider.StackRef, image string) {
	t.Helper()
	if err := stackrecords.Write(context.Background(), p.KeyValues(), ref.Tier, ref.Project, ref.Name,
		stackrecords.Stack{Kind: provider.StackApp, App: ref.Name.App, Image: image}); err != nil {
		t.Fatal(err)
	}
}

func aPreviewOfTheSameApp() provider.StackRef {
	return provider.StackRef{
		Project: "shop",
		Tier:    environment.TierPreview,
		Name:    naming.AppStack("pr-7", "web", naming.NewReleaseToken("d2", "f2")),
	}
}

func TestAReconcileKeepsAnImageInTheProjectsRegistryADeployInFlightRecords(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	recordStartingImage(t, p, aPreviewOfTheSameApp(), projectRegistryImage)
	pushed := fake.NewImages()

	if err := p.ReconcileImages(context.Background(), aContainerStack(), "web", projectRegistryImage, pushed, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := pushed.Removed(); len(got) != 0 {
		t.Errorf("the reconcile removed %v, want nothing: a deploy records the image it is about to run before its revision exists", got)
	}
}

func TestAReconcileLeavesTaggedAnImageADeployInFlightRecords(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	recordStartingImage(t, p, aPreviewOfTheSameApp(), shopWebImage+"new")

	if err := p.ReconcileImages(context.Background(), aContainerStack(), "web", shopWebImage+"new", nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := server.untags(); len(got) != 0 {
		t.Errorf("the reconcile untagged %v, want nothing: a deploy records the image it is about to run before its revision exists", got)
	}
}

func TestAReconcileOfAFailedReleaseRemovesTheImageItsOwnRecordNames(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	recordStartingImage(t, p, aContainerStack(), projectRegistryImage)
	pushed := fake.NewImages()

	if err := p.ReconcileImages(context.Background(), aContainerStack(), "web", projectRegistryImage, pushed, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got, want := pushed.Removed(), []string{projectRegistryImage}; !slices.Equal(got, want) {
		t.Errorf("the reconcile removed %v, want %v: the stack being reconciled is the one that failed to run it", got, want)
	}
}

func TestAReconcileLeavesTaggedAnImagePushedSinceItReadTheRecords(t *testing.T) {
	server := &runServer{updated: map[string]time.Time{shopWebPackage + "/tags/sha256-new": time.Now().Add(time.Hour)}}
	said := &fake.Log{}

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", shopWebImage+"new", nil, said); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := server.untags(); len(got) != 0 {
		t.Errorf("the reconcile untagged %v, want nothing: a tag pushed after the records were read may belong to a deploy those records could not show yet", got)
	}
	if !strings.Contains(strings.Join(said.Lines(), "\n"), "sha256-new") {
		t.Errorf("the reconcile said %v, want a line naming the tag it left", said.Lines())
	}
}

func TestPruningASupersededRevisionRemovesTheImageItRanFromTheProjectsRegistry(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	image := fake.RegistryServer + "/acme/shop.web-checkout:sha256-one"
	first := functionRelease("d1", image)
	one := released.provision(t, first)
	active := released.provision(t, functionRelease("d2", fake.RegistryServer+"/acme/shop.web-checkout:sha256-two"))
	if _, err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}
	pushed := fake.NewImages()

	if _, err := p.RemoveFunctionRevisions(context.Background(), first.Ref, []provider.Function{one}, pushed, nil); err != nil {
		t.Fatalf("RemoveFunctionRevisions = %v", err)
	}

	if got, want := pushed.Removed(), []string{image}; !slices.Equal(got, want) {
		t.Errorf("pruning removed %v, want %v: the revision was the last to run it, and the project's registry keeps it forever otherwise", got, want)
	}
}
