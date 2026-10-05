package conformance

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestANextRuntimeDirThatIsNotAnAbsoluteLinuxPathFails(t *testing.T) {
	for _, dir := range []string{"ocel/next", "./next", `C:\ocel\next`} {
		facts := provider.Facts{Vendor: "nowhere", NextRuntimeDir: dir}
		if found := findFunctionBuildFaults(facts); len(found) == 0 {
			t.Errorf("findFunctionBuildFaults(%q) = none, want it flagged: Next resolves the handler from wherever the function runs, so only an absolute path names one file", dir)
		}
	}
}

func TestAProviderThatHostsNoNextAppsDeclaresNoNextRuntimeDirAndPasses(t *testing.T) {
	if found := findFunctionBuildFaults(provider.Facts{Vendor: "nowhere"}); len(found) != 0 {
		t.Errorf("findFunctionBuildFaults() = %v, want none", found)
	}
}

func TestAnAbsoluteNextRuntimeDirPasses(t *testing.T) {
	if found := findFunctionBuildFaults(provider.Facts{Vendor: "nowhere", NextRuntimeDir: "/opt/ocel/next"}); len(found) != 0 {
		t.Errorf("findFunctionBuildFaults() = %v, want none", found)
	}
}
