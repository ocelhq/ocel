package gcp

import "github.com/ocelhq/ocel/pkg/edge"

func newCachingEdgeEnv(front edge.Edge) map[string]string {
	facts := factsOf(front)
	env := map[string]string{}
	if facts.InvalidatesByCacheTag {
		env[edge.CacheTagPurgeVar] = "1"
	}
	if facts.CachesResponses {
		env[edge.OriginFreshOnlyVar] = "1"
	}
	return env
}
