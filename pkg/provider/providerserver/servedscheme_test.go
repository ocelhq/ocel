package providerserver_test

import (
	"context"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAnEdgeServingPlainHTTPHasItsHostnamesAnnouncedListedAndStatedAsHTTP(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).ServesPlainHTTP(true)
	client, facts := configuredProvider(t, "1.0.0", vendor)
	if !facts.GetEdge().GetServesPlainHttp() {
		t.Errorf("Configure() edge facts = %v, want them to say the edge serves plain HTTP", facts.GetEdge())
	}
	bootstrappedOverRPC(t, client)
	previewBootstrapped(t, client)

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
