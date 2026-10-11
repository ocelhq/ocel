package edge

func NewCachingOriginEnv(facts Facts, release string, rules []CacheRule) map[string]string {
	env := map[string]string{}
	if facts.InvalidatesByCacheTag {
		env[CacheTagPurgeVar] = "1"
	}
	if !facts.CachesResponses {
		return env
	}
	env[OriginFreshOnlyVar] = "1"
	env[ReleaseTokenVar] = release
	if facts.RunsCode {
		env[EdgeAsksToCacheVar] = "1"
	}
	if declared := EncodeCacheRules(rules); declared != "" {
		env[CacheRulesVar] = declared
	}
	return env
}
