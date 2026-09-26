package edges

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestEveryEdgeThisProviderFrontsWithShapesWhatItProvisions(t *testing.T) {
	t.Parallel()

	site := costkit.EdgeSite{Slug: "shop", Class: edge.ClassProduction, Region: "us-east-1"}
	for _, kind := range SupportedEdges() {
		shape, err := Shape(kind, "ocel", site)
		if err != nil {
			t.Fatalf("Shape(%s) = %v", kind, err)
		}
		if shape.Vendor == "" || len(shape.Shared)+len(shape.Environment)+len(shape.Apps) == 0 {
			t.Errorf("Shape(%s) = %+v, want what the edge provisions for the site, billed to its vendor", kind, shape)
		}
	}
}

func TestAnEdgeThisProviderCannotFrontWithShapesNothing(t *testing.T) {
	t.Parallel()

	shape, err := Shape("relay", "ocel", costkit.EdgeSite{Slug: "shop", Class: edge.ClassProduction})
	if err != nil {
		t.Fatalf("Shape(relay) = %v", err)
	}
	if shape.Vendor != "" || len(shape.Shared)+len(shape.Environment)+len(shape.Apps) != 0 {
		t.Errorf("Shape(relay) = %+v, want nothing shaped for an edge this provider does not front with", shape)
	}
}
