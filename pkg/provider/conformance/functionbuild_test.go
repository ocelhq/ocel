package conformance

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAProviderDeclaringNoFunctionSizeBudgetPasses(t *testing.T) {
	if found := findFunctionBuildFaults(provider.Facts{Vendor: "nowhere"}); len(found) != 0 {
		t.Errorf("findFunctionBuildFaults() = %v, want none", found)
	}
}

func TestANegativeMaxFunctionBytesFails(t *testing.T) {
	if found := findFunctionBuildFaults(provider.Facts{Vendor: "nowhere", MaxFunctionBytes: -1}); len(found) == 0 {
		t.Error("findFunctionBuildFaults() = none, want a negative size budget flagged: no function fits it")
	}
}
