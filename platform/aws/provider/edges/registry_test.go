package edges

import (
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestSupportedEdges(t *testing.T) {
	t.Parallel()

	if got := SupportedEdges(); !slices.Equal(got, []edge.Kind{edge.KindCloudflare, edge.KindNative, edge.KindNone}) {
		t.Errorf("SupportedEdges() = %v, want cloudflare, native and none", got)
	}
}

func TestEdgeFor(t *testing.T) {
	t.Parallel()

	t.Run("a supported kind constructs an edge of that kind", func(t *testing.T) {
		t.Parallel()

		e, err := EdgeFor(edge.KindCloudflare, Deps{})
		if err != nil {
			t.Fatalf("EdgeFor(cloudflare) error = %v", err)
		}
		if e.Kind() != edge.KindCloudflare {
			t.Errorf("Kind() = %q, want cloudflare", e.Kind())
		}
	})

	t.Run("an unknown kind names the supported kinds", func(t *testing.T) {
		t.Parallel()

		e, err := EdgeFor("bogus", Deps{})
		if err == nil {
			t.Fatalf("EdgeFor(bogus) = %v, want an error", e)
		}
		if !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), string(edge.KindCloudflare)) {
			t.Errorf("err = %v, want it to name the asked-for kind and the supported kinds", err)
		}
	})

	t.Run("the none kind resolves to the API Gateway edge", func(t *testing.T) {
		t.Parallel()

		e, err := EdgeFor(edge.KindNone, Deps{})
		if err != nil {
			t.Fatalf("EdgeFor(none) error = %v", err)
		}
		if e.Kind() != edge.KindNone {
			t.Errorf("Kind() = %q, want none", e.Kind())
		}
	})

	t.Run("the native kind resolves to the CloudFront edge", func(t *testing.T) {
		t.Parallel()

		e, err := EdgeFor(edge.KindNative, Deps{})
		if err != nil {
			t.Fatalf("EdgeFor(native) error = %v", err)
		}
		if e.Kind() != edge.KindNative {
			t.Errorf("Kind() = %q, want native", e.Kind())
		}
	})
}
