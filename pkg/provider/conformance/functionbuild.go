package conformance

import (
	"fmt"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
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
	if facts.MaxFunctionBytes < 0 {
		found = append(found, fmt.Sprintf("Facts.MaxFunctionBytes is %d, which no function fits: name a positive budget, or zero for none", facts.MaxFunctionBytes))
	}
	return found
}

func answersTheFunctionBuildFacts(t *testing.T, suite Suite, configured *contractv1.ProviderFacts) {
	t.Helper()

	p := readDeclaredProvider(t, suite)
	declared := p.Facts()
	if got, want := configured.GetForwardsPorts(), p.Hooks().BindingAccess != nil; got != want {
		t.Errorf("ConfigureResponse.facts.forwards_ports = %v, want %v — the RPC answers whether Hooks().BindingAccess is set", got, want)
	}
	if got, want := configured.GetMaxFunctionBytes(), declared.MaxFunctionBytes; got != want {
		t.Errorf("ConfigureResponse.facts.max_function_bytes = %d, want %d — the RPC answers what Facts().MaxFunctionBytes declares", got, want)
	}
	front, err := p.Edges().Open(declared.DefaultEdge, nil)
	if err != nil {
		t.Fatalf("Edges().Open(%q) error = %v, want the default edge a project that names none deploys behind", declared.DefaultEdge, err)
	}
	edgeFacts := front.Facts()
	if got := configured.GetEdge().GetRunsCode(); got != edgeFacts.RunsCode {
		t.Errorf("ConfigureResponse.facts.edge.runs_code = %v, want %v — the RPC answers what the default edge's Facts().RunsCode declares", got, edgeFacts.RunsCode)
	}
	limits := configured.GetEdge().GetCacheTagLimits()
	got := edge.CacheTagLimits{TagBytes: int(limits.GetTagBytes()), Tags: int(limits.GetTags()), HeaderBytes: int(limits.GetHeaderBytes())}
	if got != edgeFacts.CacheTagLimits {
		t.Errorf("ConfigureResponse.facts.edge.cache_tag_limits = %+v, want %+v — the RPC answers what the default edge's Facts().CacheTagLimits declares", got, edgeFacts.CacheTagLimits)
	}
}
