package gcp

import (
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/provider/transform/transformtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const ownedConformanceModule = `
	import { defineTransform } from "@ocel/transforms"
	import { gcpOwnedFields } from "@ocel/transforms/gcp"
	const everything = Object.fromEntries(
		Object.entries(gcpOwnedFields).map(([kind, surfaces]) => [
			kind,
			Object.fromEntries(Object.keys(surfaces).map((key) => [key, { description: kind + " " + key }])),
		]),
	)
	export default defineTransform({ gcp: everything })
`

func TestATransformPatchesEveryResourceTheALBEdgeConstructs(t *testing.T) {
	t.Parallel()

	root := transformtest.Root(t, map[string]string{"conformance.transform.ts": ownedConformanceModule})
	pass := transformPass(root, []string{"./conformance.transform.ts"})
	constructed := map[string][]string{
		alb.TransformKindEdge:         alb.EdgeSurfaces,
		alb.TransformKindLoadBalancer: alb.LoadBalancerSurfaces,
	}

	req := surface.Request(environment.TierProduction, "", []transform.Resource{
		{Type: alb.TransformKindEdge, Name: "web", App: "web"},
		{Type: alb.TransformKindLoadBalancer, Name: "production"},
	})
	results, err := pass.Evaluate(t.Context(), req)
	if err != nil {
		t.Fatalf("evaluate the module that patches every resource @ocel/transforms lets a rule reach: %v", err)
	}
	if len(results) != len(req.Resources) {
		t.Fatalf("evaluated %d results for %d resources", len(results), len(req.Resources))
	}
	for i, result := range results {
		kind := req.Resources[i].Type
		if got, want := slices.Sorted(maps.Keys(result.Patches)), slices.Sorted(slices.Values(constructed[kind])); !slices.Equal(got, want) {
			t.Errorf("%s: @ocel/transforms patches %v, the alb edge constructs %v", kind, got, want)
		}
	}
}
