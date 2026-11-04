package alb

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/provider/transform/transformtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

const ownedListingModule = `
	import { defineTransform } from "@ocel/transforms"
	import { gcpOwnedFields } from "@ocel/transforms/gcp"
	const listed = Object.fromEntries(
		Object.entries(gcpOwnedFields).map(([kind, surfaces]) => [
			kind,
			Object.fromEntries(Object.entries(surfaces).map(([key, fields]) => [key, { description: fields.join(" ") }])),
		]),
	)
	export default defineTransform({ gcp: listed })
`

func TestTheFieldsTheALBRefusesAsOcelsOwnAreTheOnesTheTransformsPackageRefuses(t *testing.T) {
	t.Parallel()

	root := transformtest.Root(t, map[string]string{"owned.transform.ts": ownedListingModule})
	pass := transform.NodePass{Root: root, Modules: []string{"./owned.transform.ts"}, External: []string{"@pulumi/*"}}
	req := transform.Request{Provider: "gcp", EnvTier: string(environment.TierProduction), Resources: []transform.Resource{
		{Type: albresources.TransformKindEdge, Name: "web", App: "web"},
		{Type: albresources.TransformKindLoadBalancer, Name: string(environment.TierProduction)},
	}}

	results, err := pass.Evaluate(t.Context(), req)
	if err != nil {
		t.Fatalf("evaluate the module that lists the fields @ocel/transforms refuses: %v", err)
	}
	listed := map[string][]string{}
	for _, result := range results {
		for key, patch := range result.Patches {
			listed[key] = strings.Fields(patch["description"].(string))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(listed)) {
		if !slices.Equal(ownedFields[key], listed[key]) {
			t.Errorf("%s: the alb refuses %v as ocel's own, @ocel/transforms refuses %v", key, ownedFields[key], listed[key])
		}
	}
	if got, want := slices.Sorted(maps.Keys(ownedFields)), slices.Sorted(maps.Keys(listed)); !slices.Equal(got, want) {
		t.Errorf("the alb refuses owned fields on %v, @ocel/transforms on %v", got, want)
	}
}
