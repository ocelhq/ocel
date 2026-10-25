package build

import (
	"slices"

	"github.com/ocelhq/ocel/cli/internal/project"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Host struct {
	RunsServerless           bool
	MaxFunctionBytes         int64
	EdgeRunsCode             bool
	EdgeDispatchesContainers bool
	EdgeCacheTagLimits       CacheTagLimits
}

type CacheTagLimits struct {
	TagBytes    int64
	Tags        int64
	HeaderBytes int64
}

func ReadHost(facts *contractv1.ProviderFacts) Host {
	limits := facts.GetEdge().GetCacheTagLimits()
	return Host{
		RunsServerless:           slices.Contains(facts.GetComputes(), string(provider.ComputeServerless)),
		MaxFunctionBytes:         facts.GetMaxFunctionBytes(),
		EdgeRunsCode:             facts.GetEdge().GetRunsCode(),
		EdgeDispatchesContainers: slices.Contains(facts.GetEdge().GetDispatchedComputes(), string(provider.ComputeContainer)),
		EdgeCacheTagLimits: CacheTagLimits{
			TagBytes:    limits.GetTagBytes(),
			Tags:        limits.GetTags(),
			HeaderBytes: limits.GetHeaderBytes(),
		},
	}
}

func (h Host) BuildsNextContainersFromFunctions() bool {
	return h.RunsServerless || h.EdgeDispatchesContainers
}

func newAppHost(host Host, a project.App) Host {
	if !a.IsBuiltFromFunction() {
		return host
	}
	if !host.EdgeDispatchesContainers {
		return Host{}
	}
	return Host{EdgeRunsCode: host.EdgeRunsCode, EdgeDispatchesContainers: true, EdgeCacheTagLimits: host.EdgeCacheTagLimits}
}
