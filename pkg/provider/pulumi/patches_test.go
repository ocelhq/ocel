package pulumi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider/pulumi"
)

type emptyMonitor struct{}

func (emptyMonitor) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	return args.Name + "-id", args.Inputs, nil
}

func (emptyMonitor) Call(sdk.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

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

func patchedFor(t *testing.T, ref pulumi.ResourceRef, patch map[string]any) *pulumi.Patches {
	t.Helper()
	patches := pulumi.NewPatches()
	if err := patches.Add(ref, "function api's lambda", patch); err != nil {
		t.Fatal(err)
	}
	return patches
}

var lambda = pulumi.ResourceRef{Token: "aws:lambda/function:Function", Name: "shop-prod-api"}

func TestAClaimLayersAPatchOntoWhatOcelPlanned(t *testing.T) {
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
			name:  "a field ocel never set passes through",
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
			merged, claimed := patchedFor(t, lambda, tc.patch).Claim(lambda, tc.props)
			if !claimed {
				t.Fatal("Claim() passed over the resource its patch names")
			}
			if got := mergedValue(t, merged); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Claim() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestInstallRegistersATransformThatMergesTheClaimedResource(t *testing.T) {
	t.Parallel()

	patches := patchedFor(t, lambda, map[string]any{"memorySize": float64(2048)})

	registered := false
	err := sdk.RunErr(func(pctx *sdk.Context) error {
		if err := patches.Install(pctx); err != nil {
			return err
		}
		registered = true
		return nil
	}, sdk.WithMocks("shop", "prod--api", emptyMonitor{}))
	if err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if !registered {
		t.Fatal("Install() registered nothing, and a transform that registers nothing patches nothing")
	}

	merged, claimed := patches.Claim(lambda, sdk.Map{"memorySize": sdk.Int(512)})
	if !claimed {
		t.Fatal("the registered transform passed over the resource its patch names")
	}
	if got := mergedValue(t, merged["memorySize"]); got != float64(2048) {
		t.Errorf("memorySize = %#v, want the patched 2048", got)
	}
	if err := patches.RefuseUnclaimed(); err != nil {
		t.Errorf("RefuseUnclaimed() = %v after the patch was claimed", err)
	}
}

func TestAPatchNothingInTheProgramMatchesIsRefusedRatherThanDropped(t *testing.T) {
	t.Parallel()

	patches := patchedFor(t, lambda, map[string]any{"memorySize": float64(2048)})
	if err := sdk.RunErr(patches.Install, sdk.WithMocks("shop", "prod--api", emptyMonitor{})); err != nil {
		t.Fatalf("Install() = %v", err)
	}

	err := patches.RefuseUnclaimed()
	if err == nil {
		t.Fatal("a patch that reached no resource passed silently, and a transform nothing applies is a deploy the user did not get")
	}
	if !strings.Contains(err.Error(), "function api's lambda") {
		t.Errorf("RefuseUnclaimed() = %v, want the patch named by where it was written", err)
	}
}

func TestAResourceSharingANameWithAPatchedOneIsLeftAlone(t *testing.T) {
	t.Parallel()

	shared := pulumi.ResourceRef{Token: "aws:lambda/function:Function", Name: "shared"}
	patches := patchedFor(t, shared, map[string]any{"memorySize": float64(2048)})

	if _, claimed := patches.Claim(pulumi.ResourceRef{Token: "aws:cloudwatch/logGroup:LogGroup", Name: "shared"}, sdk.Map{}); claimed {
		t.Fatal("a log group claimed the lambda's patch because they share a name")
	}
}

func TestTwoSitesGivingOneResourceDifferentValuesIsRefused(t *testing.T) {
	t.Parallel()

	role := pulumi.ResourceRef{Token: "aws:iam/role:Role", Name: "shop-prod-api"}
	patches := pulumi.NewPatches()
	if err := patches.Add(role, "function users's role", map[string]any{"path": "/one/"}); err != nil {
		t.Fatal(err)
	}
	if err := patches.Add(role, "function orders's role", map[string]any{"path": "/another/"}); err == nil {
		t.Fatal("two sites gave the role they share two paths and the last one silently won")
	}

	agreed := pulumi.NewPatches()
	for _, site := range []string{"function users's role", "function orders's role"} {
		if err := agreed.Add(role, site, map[string]any{"path": "/one/"}); err != nil {
			t.Errorf("Add(%s) = %v, want the same value written twice accepted", site, err)
		}
	}
}

func TestNoPatchesInstallAndRefuseNothing(t *testing.T) {
	t.Parallel()

	var none *pulumi.Patches
	if !none.IsEmpty() {
		t.Error("IsEmpty() = false for no patches")
	}
	if err := sdk.RunErr(none.Install, sdk.WithMocks("shop", "prod--api", emptyMonitor{})); err != nil {
		t.Errorf("Install() = %v, want nothing registered", err)
	}
	if err := none.RefuseUnclaimed(); err != nil {
		t.Errorf("RefuseUnclaimed() = %v, want nothing refused", err)
	}
}
