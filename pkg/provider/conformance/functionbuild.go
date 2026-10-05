package conformance

import (
	"fmt"
	"path"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func RunFunctionBuild(t *testing.T, facts provider.Facts) {
	t.Helper()

	for _, fault := range findFunctionBuildFaults(facts) {
		t.Error(fault)
	}
}

func findFunctionBuildFaults(facts provider.Facts) []string {
	var found []string
	if dir := facts.NextRuntimeDir; dir != "" && !path.IsAbs(dir) {
		found = append(found, fmt.Sprintf("Facts.NextRuntimeDir is %q, and Next loads its runtime files from wherever the function runs, so only an absolute Linux path names one file", dir))
	}
	if facts.MaxFunctionBytes < 0 {
		found = append(found, fmt.Sprintf("Facts.MaxFunctionBytes is %d, which no function fits: name a positive budget, or zero for none", facts.MaxFunctionBytes))
	}
	return found
}

func answersTheFunctionBuildFacts(t *testing.T, suite Suite, configured *contractv1.ProviderFacts) {
	t.Helper()

	declared := readDeclaredFacts(t, suite)
	if got, want := configured.GetNextRuntimeDir(), declared.NextRuntimeDir; got != want {
		t.Errorf("ConfigureResponse.facts.next_runtime_dir = %q, want %q — the RPC answers what Facts().NextRuntimeDir declares", got, want)
	}
	if got, want := configured.GetMaxFunctionBytes(), declared.MaxFunctionBytes; got != want {
		t.Errorf("ConfigureResponse.facts.max_function_bytes = %d, want %d — the RPC answers what Facts().MaxFunctionBytes declares", got, want)
	}
}
