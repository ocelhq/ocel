package build

import (
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Host struct {
	MaxFunctionBytes   int64
	EdgeRunsCode       bool
	EdgeCacheTagLimits CacheTagLimits
}

type CacheTagLimits struct {
	TagBytes    int64
	Tags        int64
	HeaderBytes int64
}

func ReadHost(facts *contractv1.ProviderFacts) Host {
	limits := facts.GetEdge().GetCacheTagLimits()
	return Host{
		MaxFunctionBytes: facts.GetMaxFunctionBytes(),
		EdgeRunsCode:     facts.GetEdge().GetRunsCode(),
		EdgeCacheTagLimits: CacheTagLimits{
			TagBytes:    limits.GetTagBytes(),
			Tags:        limits.GetTags(),
			HeaderBytes: limits.GetHeaderBytes(),
		},
	}
}

func FindNextFunctionApps(apps []project.App) []string {
	var names []string
	for _, a := range FunctionApps(apps) {
		if a.Framework() == buildoutput.FrameworkNext {
			names = append(names, a.Name)
		}
	}
	return names
}
