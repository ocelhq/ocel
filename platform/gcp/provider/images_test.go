package gcp

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type handedToken string

func (t handedToken) Token(context.Context) (string, error) { return string(t), nil }

func pushing(t *testing.T, endpoint string) *Provider {
	t.Helper()
	names := Names{namespace: provider.Namespace("ocel"), project: "acme-prod"}
	return &Provider{
		options:   Options{Project: names.project, Region: "europe-west1"},
		tokens:    handedToken("ya29.stub"),
		endpoint:  endpoint,
		namespace: names.namespace,
		resolved:  &clients{Names: names, region: "europe-west1", endpoint: endpoint},
	}
}

func names(t *testing.T, p *Provider) Names {
	t.Helper()
	named, err := p.Names(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return named
}

func TestEachTierPushesToTheRepositoryItsBootstrapProvisioned(t *testing.T) {
	p := pushing(t, "")

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		target, err := p.EnsureImageRegistry(context.Background(), tier, []string{"web"})
		if err != nil {
			t.Fatalf("ImageRegistry(%s) = %v", tier, err)
		}
		if target.Server != "europe-west1-docker.pkg.dev" {
			t.Errorf("ImageRegistry(%s) server = %q, want the region's own Artifact Registry host", tier, target.Server)
		}
		if want := "acme-prod/" + p.resolved.Repository(tier); target.Namespace != want {
			t.Errorf("ImageRegistry(%s) namespace = %q, want %q", tier, target.Namespace, want)
		}
		if target.Username != "oauth2accesstoken" {
			t.Errorf("ImageRegistry(%s) username = %q, want the name Artifact Registry takes a bearer token under", tier, target.Username)
		}
		if target.Password != "ya29.stub" {
			t.Errorf("ImageRegistry(%s) password = %q, want the access token this deploy has", tier, target.Password)
		}
	}
	if p.resolved.Repository(environment.TierProduction) == p.resolved.Repository(environment.TierPreview) {
		t.Error("both tiers push to one repository, and a tier keeps its images apart from the other tier's")
	}
}

func TestTheCoordinateAnImageLandsUnderIsTheRepositoryPathTheBootstrapNames(t *testing.T) {
	p := pushing(t, "")

	target, err := p.EnsureImageRegistry(context.Background(), environment.TierProduction, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	coordinate := target.ImageRef("web", "sha256-abc")
	want := p.resolved.RepositoryPath("europe-west1", environment.TierProduction) + "/web:sha256-abc"
	if coordinate != want {
		t.Errorf("an image lands at %q, want %q", coordinate, want)
	}
}

func TestAnEmulatedDeployLoadsItsImagesIntoTheDaemonTheEmulatorShares(t *testing.T) {
	ctx := context.Background()
	emulated := pushing(t, "http://127.0.0.1:4588")

	direct, err := emulated.OpenDirectImages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(direct.Destination(), "daemon") {
		t.Errorf("OpenDirectImages() = %v, want the local docker daemon the emulator runs containers out of", direct)
	}

	target, err := emulated.EnsureImageRegistry(ctx, environment.TierProduction, []string{"web"})
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

	target, err := p.EnsureImageRegistry(ctx, environment.TierProduction, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Server == "" {
		t.Fatalf("ImageRegistry() = %v, want a registry a real deploy pushes to", target)
	}
	if p.Hooks().OpenRegistryImages != nil {
		t.Error("the provider sets an OpenRegistryImages hook, and Artifact Registry takes its push from providerserver's own registry store")
	}
}
