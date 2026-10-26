package deploy

import (
	"strings"
	"testing"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider/transform"
)

func mergedValue(t *testing.T, input sdk.Input) any {
	t.Helper()
	switch value := input.(type) {
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
	return input
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

func TestTwoCandidatesGivingOneSharedResourceDifferentValuesIsRefused(t *testing.T) {
	t.Parallel()

	stack := specUnderTransform().Ref.Name
	candidates := []transformCandidate{
		{key: resourceKey{Type: transformTypeFunction, Name: "fn--api--users"}, names: mustFunctionNames(t, stack, "fn--api--users")},
		{key: resourceKey{Type: transformTypeFunction, Name: "fn--api--orders"}, names: mustFunctionNames(t, stack, "fn--api--orders")},
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

	candidate, err := functionCandidate(testNamespace, "shop", specUnderTransform().Ref.Name, "fn--api--users")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []transformCandidate{candidate}

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
	indexed, err := indexPatches(candidates, results)
	if err != nil {
		t.Fatalf("indexPatches() = %v, want a fully named VPC placement accepted", err)
	}
	if !indexed.placesInVPC("fn--api--users") {
		t.Error("the function was not marked as placed in a VPC, so its role would go without VPC access")
	}
}
