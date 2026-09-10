package deploy

import (
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
	"github.com/ocelhq/ocel/platform/aws/provider/transform/transformtest"
)

const conformanceModule = `
	import { defineTransform } from "@ocel/transforms"
	import { awsOwnedFields } from "@ocel/transforms/aws"
	const everything = Object.fromEntries(
		Object.entries(awsOwnedFields).map(([type, keys]) => [
			type,
			Object.fromEntries(Object.keys(keys).map((key) => [key, {}])),
		]),
	)
	export default defineTransform({ aws: everything })
`

func TestSurfaceConformance(t *testing.T) {
	t.Parallel()

	rendered := map[string]map[string]resourceRef{
		transformTypeFunction: functionResourceNames("proj", naming.StackName{Env: "prod", App: "api"}, "api"),
		transformTypeBucket:   bucketResourceNames("proj", "prod", "uploads"),
		transformTypePostgres: postgresResourceNames("proj", "prod", "main"),
	}

	root := transformtest.Root(t, map[string]string{"conformance.transform.ts": conformanceModule})

	req := transform.Request{Provider: transform.Provider, EnvClass: "production", Env: "prod"}
	var candidates []transformCandidate
	for _, kind := range slices.Sorted(maps.Keys(rendered)) {
		req.Resources = append(req.Resources, transform.Resource{Type: kind, Name: kind + "-under-test"})
		candidates = append(candidates, transformCandidate{
			key:   resourceKey{Type: kind, Name: kind + "-under-test"},
			names: rendered[kind],
		})
	}

	results, err := (transform.NodePass{
		Root:    root,
		Modules: []string{"./conformance.transform.ts"},
	}).Evaluate(t.Context(), req)
	if err != nil {
		t.Fatalf("evaluate the module that patches every key: %v", err)
	}

	for i, result := range results {
		want := slices.Sorted(maps.Keys(rendered[candidates[i].key.Type]))
		got := slices.Sorted(maps.Keys(result.Patches))
		if !slices.Equal(got, want) {
			t.Errorf("%s: the module targets %v, the provider constructs %v", candidates[i].key.Type, got, want)
		}
	}

	filled := make([]transform.Result, len(results))
	wanted := map[resourceRef]bool{}
	for i, result := range results {
		patches := transform.Patches{}
		for key := range result.Patches {
			patches[key] = map[string]any{"description": candidates[i].key.Type + " " + key}
			wanted[rendered[candidates[i].key.Type][key]] = true
		}
		filled[i] = transform.Result{Patches: patches}
	}

	held, err := indexPatches(candidates, filled)
	if err != nil {
		t.Fatalf("index the patches every key carries: %v", err)
	}
	for ref := range wanted {
		if _, registered := held.patches[ref]; !registered {
			t.Errorf("nothing was registered for %s %s, so a patch on it would reach no resource", ref.Token, ref.Name)
		}
	}
	if len(held.patches) != len(wanted) {
		t.Errorf("indexPatches registered %d resources for %d keys", len(held.patches), len(wanted))
	}
}
