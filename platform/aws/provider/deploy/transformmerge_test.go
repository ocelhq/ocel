package deploy

import (
	"errors"
	"strings"
	"testing"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
)

func TestAnOutputThatResolvedToNothingNeverLandsInAPatch(t *testing.T) {
	t.Parallel()

	if !emptyOutput(nil) {
		t.Fatal("emptyOutput(nil) = false, and a record carrying an explicit null would land in the patch as one")
	}

	stack := planUnderTransform().Ref.Name
	candidates := []transformCandidate{
		{key: resourceKey{Type: transformTypeFunction, Name: "fn--api--users"}, names: functionResourceNames("shop", stack, "fn--api--users")},
	}
	results := []transform.Result{{Patches: transform.Patches{
		"lambda": map[string]any{"description": placeholderFor(customBindingType, "legacy", "subnetIds")},
	}}}

	err := resolvePlanOutputs(t.Context(), providerkit.StackPlan{
		Bindings: &publishedReader{bindings: []providerkit.Binding{
			{Type: providerkit.BindingPostgres, Name: "legacy", Properties: map[string]string{"subnetIds": ""}},
		}},
	}, candidates, results)
	var empty *EmptyOutputError
	if !errors.As(err, &empty) {
		t.Fatalf("resolvePlanOutputs() = %v, want an EmptyOutputError rather than a null in the patch", err)
	}
}

func mergedValue(t *testing.T, held sdk.Input) any {
	t.Helper()
	switch value := held.(type) {
	case sdk.String:
		return string(value)
	case sdk.Float64:
		return float64(value)
	case sdk.Int:
		return int(value)
	case sdk.Bool:
		return bool(value)
	case sdk.AnyOutput:
		return awaited(t, value)
	case sdk.Map:
		out := map[string]any{}
		for key, item := range value {
			out[key] = mergedValue(t, item)
		}
		return out
	case sdk.Array:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = mergedValue(t, item)
		}
		return out
	}
	return held
}

func awaited(t *testing.T, out sdk.AnyOutput) any {
	t.Helper()
	done := make(chan any, 1)
	out.ApplyT(func(v any) any {
		done <- v
		return v
	})
	select {
	case v := <-done:
		return v
	case <-t.Context().Done():
		t.Fatal("the merged value never resolved")
		return nil
	}
}

func TestTheMergeLayersAPatchOntoWhatOcelPlanned(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		props sdk.Map
		patch map[string]any
		want  map[string]any
	}{
		{
			name:  "a nested map keeps the siblings ocel filled",
			props: sdk.Map{"scaling": sdk.Map{"minCapacity": sdk.Float64(0.5), "maxCapacity": sdk.Float64(4)}},
			patch: map[string]any{"scaling": map[string]any{"minCapacity": float64(2)}},
			want:  map[string]any{"scaling": map[string]any{"minCapacity": float64(2), "maxCapacity": float64(4)}},
		},
		{
			name:  "a scalar replaces what ocel planned",
			props: sdk.Map{"memorySize": sdk.Int(512)},
			patch: map[string]any{"memorySize": float64(2048)},
			want:  map[string]any{"memorySize": float64(2048)},
		},
		{
			name:  "an array replaces rather than appends",
			props: sdk.Map{"subnetIds": sdk.Array{sdk.String("a"), sdk.String("b")}},
			patch: map[string]any{"subnetIds": []any{"c"}},
			want:  map[string]any{"subnetIds": []any{"c"}},
		},
		{
			name:  "a field ocel never set is carried through",
			props: sdk.Map{"memorySize": sdk.Int(512)},
			patch: map[string]any{"timeout": float64(60)},
			want:  map[string]any{"memorySize": 512, "timeout": float64(60)},
		},
		{
			name:  "a nested patch over a value that is not a map replaces it whole",
			props: sdk.Map{"vpcConfig": sdk.String("none")},
			patch: map[string]any{"vpcConfig": map[string]any{"subnetIds": []any{"a"}}},
			want:  map[string]any{"vpcConfig": map[string]any{"subnetIds": []any{"a"}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mergedValue(t, mergeProps(tc.props, tc.patch))
			held, mapped := got.(map[string]any)
			if !mapped {
				t.Fatalf("mergeProps() = %#v, want a map", got)
			}
			if len(held) != len(tc.want) {
				t.Fatalf("mergeProps() = %#v, want %#v", held, tc.want)
			}
			for key, want := range tc.want {
				if !sameValue(held[key], want) {
					t.Errorf("mergeProps()[%q] = %#v, want %#v", key, held[key], want)
				}
			}
		})
	}
}

func patchedFor(ref resourceRef, patch map[string]any) *transformPatches {
	return &transformPatches{
		patches: map[resourceRef]map[string]any{ref: patch},
		sites:   map[resourceRef]string{ref: "function api's lambda"},
	}
}

func TestInstallRegistersATransformThatMergesTheClaimedResource(t *testing.T) {
	t.Parallel()

	ref := resourceRef{Token: tokenLambdaFunction, Name: "shop-prod-api"}
	held := patchedFor(ref, map[string]any{"memorySize": float64(2048)})

	registered := false
	err := sdk.RunErr(func(pctx *sdk.Context) error {
		if err := held.install(pctx); err != nil {
			return err
		}
		registered = true
		return nil
	}, sdk.WithMocks("shop", "prod--api", &inputRecorder{}))
	if err != nil {
		t.Fatalf("install() = %v", err)
	}
	if !registered {
		t.Fatal("install() registered nothing, and a transform that registers nothing patches nothing")
	}

	merged, claimed := held.claim(ref, sdk.Map{"memorySize": sdk.Int(512)})
	if !claimed {
		t.Fatal("the registered transform passed over the resource its patch names")
	}
	if got := mergedValue(t, merged["memorySize"]); !sameValue(got, float64(2048)) {
		t.Errorf("memorySize = %#v, want the patched 2048", got)
	}
	if err := held.refuseUnclaimed(); err != nil {
		t.Errorf("refuseUnclaimed() = %v after the patch was claimed", err)
	}
}

func TestAPatchNothingInTheProgramMatchesIsRefusedRatherThanDroppped(t *testing.T) {
	t.Parallel()

	ref := resourceRef{Token: tokenLambdaFunction, Name: "shop-prod-api"}
	held := patchedFor(ref, map[string]any{"memorySize": float64(2048)})

	if err := sdk.RunErr(func(pctx *sdk.Context) error {
		return held.install(pctx)
	}, sdk.WithMocks("shop", "prod--api", &inputRecorder{})); err != nil {
		t.Fatalf("install() = %v", err)
	}

	err := held.refuseUnclaimed()
	if err == nil {
		t.Fatal("a patch that reached no resource passed silently, and a transform nothing carries out is a deploy the user did not get")
	}
	if !strings.Contains(err.Error(), "function api's lambda") {
		t.Errorf("refuseUnclaimed() = %v, want the patch named by where it was written", err)
	}
}

func TestAResourceSharingANameWithAPatchedOneIsLeftAlone(t *testing.T) {
	t.Parallel()

	held := patchedFor(resourceRef{Token: tokenLambdaFunction, Name: "shared"}, map[string]any{"memorySize": float64(2048)})
	held.claimed = map[resourceRef]bool{}

	if _, claimed := held.claim(resourceRef{Token: tokenLogGroup, Name: "shared"}, sdk.Map{}); claimed {
		t.Fatal("a log group claimed the lambda's patch because they share a name")
	}
}

func TestTwoCandidatesGivingOneSharedResourceDifferentValuesIsRefused(t *testing.T) {
	t.Parallel()

	stack := planUnderTransform().Ref.Name
	candidates := []transformCandidate{
		{key: resourceKey{Type: transformTypeFunction, Name: "fn--api--users"}, names: functionResourceNames("shop", stack, "fn--api--users")},
		{key: resourceKey{Type: transformTypeFunction, Name: "fn--api--orders"}, names: functionResourceNames("shop", stack, "fn--api--orders")},
	}
	results := []transform.Result{
		{Patches: transform.Patches{"role": map[string]any{"path": "/one/"}}},
		{Patches: transform.Patches{"role": map[string]any{"path": "/another/"}}},
	}

	if _, err := indexPatches(candidates, results); err == nil {
		t.Fatal("two functions gave the role they share two paths and the last one silently won")
	}

	agreed := []transform.Result{
		{Patches: transform.Patches{"role": map[string]any{"path": "/one/"}}},
		{Patches: transform.Patches{"role": map[string]any{"path": "/one/"}}},
	}
	if _, err := indexPatches(candidates, agreed); err != nil {
		t.Errorf("indexPatches() = %v, want the same value written twice accepted", err)
	}
}

func TestAFunctionPlacedInAVPCWithHalfOfWhatALambdaNeedsIsRefused(t *testing.T) {
	t.Parallel()

	stack := planUnderTransform().Ref.Name
	candidates := []transformCandidate{
		{key: resourceKey{Type: transformTypeFunction, Name: "fn--api--users"}, names: functionResourceNames("shop", stack, "fn--api--users")},
	}

	for _, tc := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{name: "no subnets", config: map[string]any{"securityGroupIds": []any{"sg-1"}}, want: "no subnets"},
		{name: "no security groups", config: map[string]any{"subnetIds": []any{"subnet-1"}}, want: "no security group"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			results := []transform.Result{{Patches: transform.Patches{
				"lambda": map[string]any{lambdaVPCConfigField: tc.config},
			}}}
			_, err := indexPatches(candidates, results)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("indexPatches() = %v, want a refusal naming %q rather than a raw API error from AWS", err, tc.want)
			}
		})
	}

	results := []transform.Result{{Patches: transform.Patches{
		"lambda": map[string]any{lambdaVPCConfigField: map[string]any{
			"subnetIds": []any{"subnet-1"}, "securityGroupIds": []any{"sg-1"},
		}},
	}}}
	held, err := indexPatches(candidates, results)
	if err != nil {
		t.Fatalf("indexPatches() = %v, want a fully named VPC placement accepted", err)
	}
	if !held.placesInVPC("fn--api--users") {
		t.Error("the function was not marked as placed in a VPC, so its role would go without VPC access")
	}
}
