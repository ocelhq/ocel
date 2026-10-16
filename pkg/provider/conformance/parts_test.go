package conformance

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestTheSuiteAppliesOnlyWhatTheEdgeItOpenedTheBootstrapperForRequires(t *testing.T) {
	t.Parallel()

	offered := []provider.Part{
		{Name: "state", Summary: "installed under every edge"},
		{Name: "other-front", Summary: "the front the other edge is served from", Edges: []edge.Kind{"other"}},
	}

	for kind, want := range map[edge.Kind][]string{
		"plain": {"state"},
		"other": {"state", "other-front"},
	} {
		got := applicable(offered, kind)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("the suite applies %v under the %q edge, want %v: a part an edge token gates is installed only under that edge, "+
				"and asking a bootstrap opened for another one to raise it is asking for what its provider refuses", got, kind, want)
		}
	}
}
