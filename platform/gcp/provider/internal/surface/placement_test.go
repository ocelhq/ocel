package surface_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

func TestAPlacementReadsEveryNetworkAPatchNamesThroughItsLists(t *testing.T) {
	t.Parallel()
	result := transform.Result{Patches: transform.Patches{"instance": {"settings": map[string]any{"ipConfiguration": map[string]any{"pscConfigs": []any{
		map[string]any{"pscAutoConnections": []any{
			map[string]any{"consumerNetwork": "projects/acme/global/networks/shared"},
			map[string]any{"consumerNetwork": "projects/acme/global/networks/other"},
		}},
	}}}}}}

	named, set := surface.ReadPlacement(result, surface.DatabasePlacement)

	if !set || !slices.Equal(named, []string{"projects/acme/global/networks/shared", "projects/acme/global/networks/other"}) {
		t.Errorf("readPlacement() = %v, %t, want both networks the patch names", named, set)
	}
}

func TestAPlacementFilledFromABindingIsSetToANetworkNotYetKnown(t *testing.T) {
	t.Parallel()
	output := map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "network", "property": "id"}}
	result := transform.Result{Patches: transform.Patches{"instance": {"desiredAutoCreatedEndpoints": []any{map[string]any{"network": output}}}}}

	named, set := surface.ReadPlacement(result, surface.StorePlacement)

	if !set || !slices.Equal(named, []string{""}) {
		t.Errorf("readPlacement() = %q, %t, want it set to one network not known until the binding is read", named, set)
	}
}

func TestAPatchThatPlacesNothingLeavesThePlacementUnset(t *testing.T) {
	t.Parallel()
	result := transform.Result{Patches: transform.Patches{"service": {"template": map[string]any{"timeout": "30s"}}}}

	if named, set := surface.ReadPlacement(result, surface.EgressPlacement); set || len(named) != 0 {
		t.Errorf("readPlacement() = %v, %t, want nothing set", named, set)
	}
}

func TestAnAllowListHasAPlacementOnlyWhenItTakesAPathUnderIt(t *testing.T) {
	t.Parallel()
	allowed := surface.Allowed{"service": {"template.timeout", "template.vpcAccess.networkInterfaces.network"}}

	if !allowed.HasPlacement(surface.EgressPlacement) || allowed.HasPlacement(surface.StorePlacement) || (surface.Allowed{"service": {"template.vpcAccessible"}}).HasPlacement(surface.EgressPlacement) {
		t.Error("hasPlacement() reads the allow-list wrongly: it takes template.vpcAccess.* and nothing else under that name")
	}
}
