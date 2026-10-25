package release

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func newCachingEdgeEnv(spec provider.StackSpec) map[string]string {
	return edge.NewCachingOriginEnv(run.FactsOf(spec.Edge), spec.Ref.Name.Release.String())
}
