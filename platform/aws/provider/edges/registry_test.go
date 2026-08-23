package edges

import (
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/platform/aws/provider/certs"

	"github.com/ocelhq/ocel/pkg/providerkit/conformance"

	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestRegistryConformance(t *testing.T) {
	t.Parallel()

	conformance.RunEdgeRegistry(t, Registry{})
}

func TestIgnoredPinNote(t *testing.T) {
	t.Parallel()

	const host = "shop.app.com"
	const arn = "arn:aws:acm:us-east-1:111122223333:certificate/pinned"
	registry := Registry{Deps: Deps{Certificates: map[string]string{host: arn}}}

	t.Run("an edge that terminates TLS with a certificate of its own says the pin is ignored", func(t *testing.T) {
		t.Parallel()

		front, err := registry.Open(cloudflare.Kind)
		if err != nil {
			t.Fatalf("Open(cloudflare): %v", err)
		}
		note := IgnoredPinNote(front, registry.Certifier(front, certs.Deps{}), host)
		if !strings.Contains(note, "ignored") || !strings.Contains(note, host) || !strings.Contains(note, string(cloudflare.Kind)) {
			t.Errorf("note = %q, want the ignored pin said out loud, naming the host and the edge", note)
		}
	})

	t.Run("an edge ocel certifies keeps the pin", func(t *testing.T) {
		t.Parallel()

		front, err := registry.Open(cloudfront.Kind)
		if err != nil {
			t.Fatalf("Open(cloudfront): %v", err)
		}
		certifier := registry.Certifier(front, certs.Deps{AWS: aws.Config{Region: "eu-west-1"}})
		if !certifier.Issues() {
			t.Fatal("Issues() = false, want the certifier of an edge ocel requests certificates for")
		}
		if certifier.PinFor(host) != arn {
			t.Errorf("PinFor(%s) = %q, want the operator's pin read from this provider's options", host, certifier.PinFor(host))
		}
		if note := IgnoredPinNote(front, certifier, host); note != "" {
			t.Errorf("note = %q, want nothing said about a pin this edge uses", note)
		}
	})
}

func TestSupportedEdges(t *testing.T) {
	t.Parallel()

	if got := SupportedEdges(); !slices.Equal(got, []edge.Kind{apigateway.Kind, cloudflare.Kind, cloudfront.Kind}) {
		t.Errorf("SupportedEdges() = %v, want api-gateway, cloudflare and cloudfront", got)
	}
}

func TestEdgeFor(t *testing.T) {
	t.Parallel()

	t.Run("a supported kind constructs an edge of that kind", func(t *testing.T) {
		t.Parallel()

		e, err := EdgeFor(cloudflare.Kind, Deps{})
		if err != nil {
			t.Fatalf("EdgeFor(cloudflare) error = %v", err)
		}
		if e.Kind() != cloudflare.Kind {
			t.Errorf("Kind() = %q, want cloudflare", e.Kind())
		}
	})

	t.Run("an unknown kind names the supported kinds", func(t *testing.T) {
		t.Parallel()

		e, err := EdgeFor("bogus", Deps{})
		if err == nil {
			t.Fatalf("EdgeFor(bogus) = %v, want an error", e)
		}
		if !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), string(cloudflare.Kind)) {
			t.Errorf("err = %v, want it to name the asked-for kind and the supported kinds", err)
		}
	})

	t.Run("the api-gateway kind resolves to the API Gateway edge", func(t *testing.T) {
		t.Parallel()

		e, err := EdgeFor(apigateway.Kind, Deps{})
		if err != nil {
			t.Fatalf("EdgeFor(api-gateway) error = %v", err)
		}
		if e.Kind() != apigateway.Kind {
			t.Errorf("Kind() = %q, want api-gateway", e.Kind())
		}
	})

	t.Run("the cloudfront kind resolves to the CloudFront edge", func(t *testing.T) {
		t.Parallel()

		e, err := EdgeFor(cloudfront.Kind, Deps{})
		if err != nil {
			t.Fatalf("EdgeFor(cloudfront) error = %v", err)
		}
		if e.Kind() != cloudfront.Kind {
			t.Errorf("Kind() = %q, want cloudfront", e.Kind())
		}
	})
}
