package deploy

import (
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/provider/transform/transformtest"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
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

func TestATransformPatchesEveryResourceTheProviderConstructs(t *testing.T) {
	t.Parallel()

	at := component.Stack{Ref: provider.StackRef{Tier: environment.TierProduction}, Namespace: testNamespace, Project: "proj", Env: "prod"}
	transformed := func(c component.Component, declared provider.Resource) map[string][]component.ResourceRef {
		target, err := c.Transform(at, declared)
		if err != nil {
			t.Fatal(err)
		}
		return target.Names
	}
	functionNames, err := function.NamesFor(testNamespace, "proj", naming.AppStack("prod", "api", fixedRelease(t)), "api")
	if err != nil {
		t.Fatal(err)
	}
	containerNames, err := container.NamesFor(testNamespace, "proj", naming.AppStack("prod", "web", fixedRelease(t)))
	if err != nil {
		t.Fatal(err)
	}
	rendered := map[string]map[string][]component.ResourceRef{
		transformTypeFunction:  functionNames.Surfaces(),
		transformTypeContainer: containerNames.Surfaces(),
		"bucket":               transformed(bucket.New(component.Artifacts{}), provider.Resource{Name: "uploads", Type: provider.BindingBucket}),
		"postgres":             transformed(postgres.New(nil), provider.Resource{Name: "main", Type: provider.BindingPostgres}),
		"kv":                   transformed(kv.New(kv.Tokens{}), provider.Resource{Name: "cache", Type: provider.BindingKV}),
		transformTypeNetwork:   network.ResourceNames(testNamespace, environment.TierProduction),
	}
	maps.Copy(rendered[transformTypeNetwork], functionnetwork.ResourceNames(testNamespace, environment.TierProduction))

	root := transformtest.Root(t, map[string]string{"conformance.transform.ts": conformanceModule})

	req := transform.Request{Provider: transformProvider, EnvTier: "production", Env: "prod"}
	var candidates []transformCandidate
	for _, kind := range slices.Sorted(maps.Keys(rendered)) {
		req.Resources = append(req.Resources, transform.Resource{Type: kind, Name: kind + "-under-test"})
		candidates = append(candidates, transformCandidate{
			key:   resourceKey{Type: kind, Name: kind + "-under-test"},
			names: rendered[kind],
		})
	}

	results, err := NodePass(root, []string{"./conformance.transform.ts"}).Evaluate(t.Context(), req)
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
	wanted := map[component.ResourceRef]bool{}
	for i, result := range results {
		patches := transform.Patches{}
		for key := range result.Patches {
			patches[key] = map[string]any{"description": candidates[i].key.Type + " " + key}
			for _, ref := range rendered[candidates[i].key.Type][key] {
				wanted[ref] = true
			}
		}
		filled[i] = transform.Result{Patches: patches}
	}

	indexed, err := indexPatches(candidates, filled)
	if err != nil {
		t.Fatalf("index the patches every key names: %v", err)
	}
	for ref := range wanted {
		if _, registered := indexed.patches[ref]; !registered {
			t.Errorf("nothing was registered for %s %s, so a patch on it would reach no resource", ref.Token, ref.Name)
		}
	}
	if len(indexed.patches) != len(wanted) {
		t.Errorf("indexPatches registered %d resources for the %d the keys name", len(indexed.patches), len(wanted))
	}
}
