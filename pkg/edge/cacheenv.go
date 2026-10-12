package edge

type EdgeCacheMode string

const (
	EdgeCacheNone   EdgeCacheMode = ""
	EdgeCacheMarked EdgeCacheMode = "marked"
	EdgeCacheAll    EdgeCacheMode = "all"
)

func NewEdgeCacheMode(facts Facts) EdgeCacheMode {
	switch {
	case !facts.CachesResponses:
		return EdgeCacheNone
	case facts.RunsCode:
		return EdgeCacheMarked
	default:
		return EdgeCacheAll
	}
}

func NewCachingOriginEnv(facts Facts, release string) map[string]string {
	env := map[string]string{}
	if facts.InvalidatesByCacheTag {
		env[CacheTagPurgeVar] = "1"
	}
	mode := NewEdgeCacheMode(facts)
	if mode == EdgeCacheNone {
		return env
	}
	env[EdgeCacheVar] = string(mode)
	env[ReleaseTokenVar] = release
	return env
}
