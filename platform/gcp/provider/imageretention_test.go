package gcp

import (
	"slices"
	"strings"
	"testing"
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
