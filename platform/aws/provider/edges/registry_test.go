package edges

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

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
