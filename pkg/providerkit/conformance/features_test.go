package conformance

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestTheSuiteAppliesOnlyWhatTheEdgeItOpenedTheBootstrapperForRequires(t *testing.T) {
	t.Parallel()

	catalogue := []providerkit.Feature{
		{Name: "state", Summary: "stands under every edge"},
		{Name: "other-front", Summary: "the front the other edge is served from", Needs: []string{providerkit.NeedsEdgePrefix + "other"}},
	}

	for kind, want := range map[edge.Kind][]string{
		"plain": {"state"},
		"other": {"state", "other-front"},
	} {
		got, err := applicable(catalogue, kind)
		if err != nil {
			t.Fatalf("applicable(%q) = %v", kind, err)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("the suite applies %v under the %q edge, want %v: a feature an edge token gates stands only under that edge, "+
				"and asking a bootstrapper opened for another one to raise it is asking for what its provider refuses", got, kind, want)
		}
	}
}

func TestAFeatureAnotherFeatureDependsOnComesWithIt(t *testing.T) {
	t.Parallel()

	catalogue := []providerkit.Feature{
		{Name: "state", Summary: "what the front keeps its state in"},
		{Name: "front", Summary: "the front", DependsOn: []string{"state"}, Needs: []string{providerkit.NeedsEdgePrefix + "other"}},
	}

	got, err := applicable(catalogue, "other")
	if err != nil {
		t.Fatalf("applicable(other) = %v", err)
	}
	if !slices.Contains(got, "state") || !slices.Contains(got, "front") {
		t.Errorf("the suite applies %v, want the gated feature and what it depends on: an apply missing a dependency is refused", got)
	}
}
