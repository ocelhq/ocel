package providerserver_test

import (
	"context"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func ensuredPreviewURL(t *testing.T, client contractv1connect.ProviderServiceClient) string {
	t.Helper()
	ensured, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       "abcdefghijklmnop",
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
	})
	if err != nil {
		t.Fatalf("EnsurePreviewAlias: %v", err)
	}
	return ensured.GetUrls()["web"]
}

func TestAnEdgeServingPlainHTTPHasItsPreviewAnsweredAnnouncedAndListedAsHTTP(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).ServesPlainHTTP(true)
	client, _ := configuredProvider(t, "1.0.0", vendor)
	bootstrappedOverRPC(t, client)
	previewBootstrapped(t, client)
	if url := ensuredPreviewURL(t, client); !strings.HasPrefix(url, "http://pr-7-") {
		t.Errorf("EnsurePreviewAlias answered %q, want an http:// URL of the preview", url)
	}

	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	urls := append(servedURLs(result), findDeploymentURL(result, "web"))
	for _, url := range urls {
		if !strings.HasPrefix(url, "http://pr-7-") {
			t.Errorf("the deploy announced %q, want an http:// URL of the preview", url)
		}
	}
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.GetEnvironments(); len(got) != 1 || len(got[0].GetAliasUrls()) == 0 || !strings.HasPrefix(got[0].GetAliasUrls()[0], "http://pr-7-") {
		t.Errorf("ListEnvironments = %v, want the alias listed as an http:// URL", got)
	}
}

func TestAnEdgeServingOnAPortOfItsOwnHasItsPreviewAnsweredAnnouncedAndListedWithThePort(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	front := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	front.ServesPlainHTTP(true)
	front.ServesOnPort(8130)
	client, _ := configuredProvider(t, "1.0.0", vendor)
	bootstrappedOverRPC(t, client)
	previewBootstrapped(t, client)
	if url := ensuredPreviewURL(t, client); !strings.HasPrefix(url, "http://pr-7-") || !strings.HasSuffix(url, ":8130") {
		t.Errorf("EnsurePreviewAlias answered %q, want the preview's http:// URL on port 8130", url)
	}

	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	for _, url := range append(servedURLs(result), findDeploymentURL(result, "web")) {
		if !strings.HasPrefix(url, "http://pr-7-") || !strings.HasSuffix(url, ":8130") {
			t.Errorf("the deploy announced %q, want the preview's http:// URL on port 8130", url)
		}
	}
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.GetEnvironments(); len(got) != 1 || len(got[0].GetAliasUrls()) == 0 || !strings.HasSuffix(got[0].GetAliasUrls()[0], ":8130") {
		t.Errorf("ListEnvironments = %v, want the alias listed on port 8130", got)
	}
}
