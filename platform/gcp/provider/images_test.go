package gcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type handedToken string

func (t handedToken) Token(context.Context) (string, error) { return string(t), nil }

func pushing(t *testing.T, endpoint string) *Provider {
	t.Helper()
	names := Names{namespace: providerkit.Namespace("ocel"), project: "acme-prod"}
	return &Provider{
		options:  Options{Project: names.project, Region: "europe-west1"},
		tokens:   handedToken("ya29.stub"),
		endpoint: endpoint,
		clients:  &clients{Names: names, region: "europe-west1", endpoint: endpoint},
	}
}

func TestEachClassPushesToTheRepositoryItsBootstrapStoodUp(t *testing.T) {
	p := pushing(t, "")

	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		target, err := p.ImageRegistry(context.Background(), class, []string{"web"})
		if err != nil {
			t.Fatalf("ImageRegistry(%s) = %v", class, err)
		}
		if target.Server != "europe-west1-docker.pkg.dev" {
			t.Errorf("ImageRegistry(%s) server = %q, want the region's own Artifact Registry host", class, target.Server)
		}
		if want := "acme-prod/" + p.Names().Repository(class); target.Namespace != want {
			t.Errorf("ImageRegistry(%s) namespace = %q, want %q", class, target.Namespace, want)
		}
		if target.Username != "oauth2accesstoken" {
			t.Errorf("ImageRegistry(%s) username = %q, want the name Artifact Registry takes a bearer token under", class, target.Username)
		}
		if target.Password != "ya29.stub" {
			t.Errorf("ImageRegistry(%s) password = %q, want the access token this deploy holds", class, target.Password)
		}
	}
	if p.Names().Repository(providerkit.ClassProduction) == p.Names().Repository(providerkit.ClassPreview) {
		t.Error("both classes push to one repository, and a class keeps its images apart from the other class's")
	}
}

func TestTheCoordinateAnImageLandsUnderIsTheRepositoryPathTheBootstrapNames(t *testing.T) {
	p := pushing(t, "")

	target, err := p.ImageRegistry(context.Background(), providerkit.ClassProduction, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	coordinate := target.Coordinate("web", "sha256-abc")
	want := p.Names().RepositoryPath("europe-west1", providerkit.ClassProduction) + "/web:sha256-abc"
	if coordinate != want {
		t.Errorf("an image lands at %q, want %q", coordinate, want)
	}
}

func TestAnEmulatedDeployLoadsItsImagesIntoTheDaemonTheEmulatorShares(t *testing.T) {
	ctx := context.Background()
	emulated := pushing(t, "http://127.0.0.1:4588")

	direct, err := emulated.DirectImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(direct.(providerkit.ImageDestination).ImageDestination(), "daemon") {
		t.Errorf("DirectImages() = %v, want the local docker daemon the emulator runs containers out of", direct)
	}

	target, err := emulated.ImageRegistry(ctx, providerkit.ClassProduction, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Server != "" {
		t.Errorf("ImageRegistry() = %+v under emulation, and no Artifact Registry is emulated: "+
			"a deploy told of no registry writes its images to the daemon instead, under the names they were built with", target)
	}
}

func TestARealDeployPushesToTheRegistryItResolved(t *testing.T) {
	ctx := context.Background()
	p := pushing(t, "")

	target, err := p.ImageRegistry(ctx, providerkit.ClassProduction, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := p.Images(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.(fmt.Stringer).String(), target.Server) {
		t.Errorf("Images() = %v, want the images pushed to %s", store, target.Server)
	}
}
