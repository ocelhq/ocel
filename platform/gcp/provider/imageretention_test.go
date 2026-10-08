package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

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

func TestAReconcileUntagsEveryTagOfTheAppsPackageNoRevisionRuns(t *testing.T) {
	server := &runServer{tagged: map[string][]string{shopWebPackage: {"sha256-old", "sha256-new", "sha256-older"}}}
	app := serves("ocel-shop-prod-web")
	app.image = shopWebImage + "new"
	released(t, server, app)

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", app.image, nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	want := []string{shopWebPackage + "/tags/sha256-old", shopWebPackage + "/tags/sha256-older"}
	if got := server.untags(); !slices.Equal(got, want) {
		t.Errorf("the reconcile untagged %v, want %v: the revision that runs sha256-new keeps its tag, and every other tag of the package is one nothing runs, which the repository's cleanup policy deletes only once it is untagged", got, want)
	}
}

func TestAReconcileOfAReleaseThatNeverStartedUntagsTheImageItPushed(t *testing.T) {
	server := &runServer{tagged: map[string][]string{shopWebPackage: {"sha256-new"}}}

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", shopWebImage+"new", nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got, want := server.untags(), []string{shopWebPackage + "/tags/sha256-new"}; !slices.Equal(got, want) {
		t.Errorf("the reconcile untagged %v, want %v: a release that failed before it created a revision leaves its image tagged forever otherwise", got, want)
	}
}

func TestAReconcileKeepsATagAServiceElsewhereInTheRegionRuns(t *testing.T) {
	server := &runServer{
		tagged:    map[string][]string{shopWebPackage: {"sha256-old"}},
		elsewhere: map[string]string{"ocel-shop-preview-web-00001": shopWebImage + "old"},
	}

	if err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", shopWebImage+"new", nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := server.untags(); len(got) != 0 {
		t.Errorf("the reconcile untagged %v, want nothing: another environment's service still runs that image", got)
	}
}

func TestAReconcileOfAnImageOutsideArtifactRegistryTouchesNothing(t *testing.T) {
	server := &runServer{tagged: map[string][]string{shopWebPackage: {"sha256-old"}}}

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

func TestAReconcileThatCannotListTheTagsSaysWhy(t *testing.T) {
	server := &runServer{refusesList: true}

	err := server.open(t).ReconcileImages(context.Background(), aContainerStack(), "web", shopWebImage+"new", nil, nil)

	if err == nil || !strings.Contains(err.Error(), "artifactregistry.tags.list") {
		t.Errorf("ReconcileImages() = %v, want the refusal Artifact Registry gave, which names the permission the credential lacks", err)
	}
}

func TestDestroyingAContainerUntagsEveryImageOfItsPackageNothingElseRuns(t *testing.T) {
	server := &runServer{tagged: map[string][]string{shopWebPackage: {"sha256-old", "sha256-new"}}}
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

	want := []string{shopWebPackage + "/tags/sha256-new", shopWebPackage + "/tags/sha256-old"}
	got := server.untags()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("destroying the container untagged %v, want %v: the repository keeps every tagged image a destroyed project ever pushed otherwise", got, want)
	}
}

func releasedOn(t *testing.T, p *Provider, s serving) {
	t.Helper()
	if _, err := p.deployService(context.Background(), s, nil); err != nil {
		t.Fatalf("deployService(%s) = %v", s.service, err)
	}
}
