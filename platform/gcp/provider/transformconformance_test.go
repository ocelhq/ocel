package gcp

import (
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/provider/transform/transformtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const conformanceModule = `
	import { defineTransform } from "@ocel/transforms"
	import { gcpAllowedPaths } from "@ocel/transforms/gcp"
	const nested = (paths) => {
		const patch = {}
		for (const path of paths) {
			const keys = path.split(".")
			let at = patch
			for (const key of keys.slice(0, -1)) at = at[key] ??= {}
			at[keys[keys.length - 1]] = "every"
		}
		return patch
	}
	const everything = Object.fromEntries(
		Object.entries(gcpAllowedPaths).map(([kind, surfaces]) => [
			kind,
			Object.fromEntries(Object.entries(surfaces).map(([key, paths]) => [key, nested(paths)])),
		]),
	)
	export default defineTransform({ gcp: everything })
`

func leafPathsOf(prefix string, value any) []string {
	object, nested := value.(map[string]any)
	if !nested {
		return []string{prefix}
	}
	var paths []string
	for key, inner := range object {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		paths = append(paths, leafPathsOf(path, inner)...)
	}
	return paths
}

func TestATransformPatchesExactlyThePathsTheProviderTranslates(t *testing.T) {
	registry := (&Provider{}).components()
	allowed := registry.Transformed()
	root := transformtest.Root(t, map[string]string{"conformance.transform.ts": conformanceModule})

	req := surface.Request(environment.TierProduction, "prod", nil)
	for _, kind := range slices.Sorted(maps.Keys(allowed)) {
		req.Resources = append(req.Resources, transform.Resource{Type: kind, Name: kind + "-under-test"})
	}
	pass := transformPass(root, []string{"./conformance.transform.ts"})

	results, err := pass.Evaluate(t.Context(), req)
	if err != nil {
		t.Fatalf("evaluate the module that sets every path @ocel/transforms allows: %v", err)
	}
	if len(results) != len(req.Resources) || len(results) == 0 {
		t.Fatalf("evaluated %d results for %d resources, want one per ocel resource the provider patches", len(results), len(req.Resources))
	}
	for i, result := range results {
		kind := req.Resources[i].Type
		if got, want := slices.Sorted(maps.Keys(result.Patches)), slices.Sorted(maps.Keys(allowed[kind])); !slices.Equal(got, want) {
			t.Errorf("%s: @ocel/transforms patches %v, the provider constructs %v", kind, got, want)
		}
		for key, patch := range result.Patches {
			got := leafPathsOf("", patch)
			slices.Sort(got)
			if want := allowed[kind][key]; !slices.Equal(got, want) {
				t.Errorf("%s.%s: @ocel/transforms allows %v, the provider translates %v", kind, key, got, want)
			}
		}
	}
}
