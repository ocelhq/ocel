package gcp

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

func newCachingEdgeEnv(spec provider.StackSpec) map[string]string {
	return edge.NewCachingOriginEnv(factsOf(spec.Edge), spec.Ref.Name.Release.String(), spec.App.Cache)
}
