package deploy

import (
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
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

	rendered := map[string]map[string]string{
		transformTypeFunction: functionResourceNames("proj", naming.StackName{Env: "prod", App: "api"}, "api"),
		transformTypeBucket: bucketResourceNames("proj", "prod", "uploads",
			translateBucket(&providerkit.BucketSpec{AllowedOrigins: []string{"https://acme.test"}})),
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

	if _, err := indexPatches(candidates, results); err != nil {
		t.Errorf("index the patches every key carries: %v", err)
	}
}
