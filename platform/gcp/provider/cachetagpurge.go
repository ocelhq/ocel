package gcp

import "github.com/ocelhq/ocel/pkg/edge"

func newCacheTagPurgeEnv(front edge.Edge) map[string]string {
	if !factsOf(front).InvalidatesByCacheTag {
		return nil
	}
	return map[string]string{edge.CacheTagPurgeVar: "1"}
}
