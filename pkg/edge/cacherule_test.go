package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type cacheRuleFixture struct {
	Rules []CacheRule `json:"rules"`
	Cases []struct {
		Path         string `json:"path"`
		Rule         int    `json:"rule"`
		CacheControl string `json:"cacheControl"`
	} `json:"cases"`
	Invalid []struct {
		Reason string          `json:"reason"`
		Rule   json.RawMessage `json:"rule"`
	} `json:"invalid"`
}

func readCacheRuleFixture(t *testing.T) cacheRuleFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "edge", "contract", "fixtures", "cache-rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture cacheRuleFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestTheFirstCacheRuleThatMatchesAPathIsTheEdgeContractFixturesAnswer(t *testing.T) {
	t.Parallel()

	fixture := readCacheRuleFixture(t)
	for _, c := range fixture.Cases {
		found, ok := FindCacheRule(fixture.Rules, c.Path)
		if !ok {
			t.Errorf("FindCacheRule(%q) found no rule, want rule %d", c.Path, c.Rule)
			continue
		}
		if want := fixture.Rules[c.Rule]; found != want {
			t.Errorf("FindCacheRule(%q) = %+v, want %+v", c.Path, found, want)
		}
		if got := found.CacheControl(); got != c.CacheControl {
			t.Errorf("CacheControl for %q = %q, want %q", c.Path, got, c.CacheControl)
		}
	}
}

func TestNoCacheRuleMatchesAPathWhenTheRulesAreEmpty(t *testing.T) {
	t.Parallel()

	if _, ok := FindCacheRule(nil, "/blog/post"); ok {
		t.Error("a build with no cache rules cached a path")
	}
}

func TestEveryCacheRuleOfTheFixtureIsAccepted(t *testing.T) {
	t.Parallel()

	if err := RefuseCacheRules(readCacheRuleFixture(t).Rules); err != nil {
		t.Fatal(err)
	}
}

func TestTheFixturesInvalidCacheRulesAreRefused(t *testing.T) {
	t.Parallel()

	for _, c := range readCacheRuleFixture(t).Invalid {
		var rule CacheRule
		if err := json.Unmarshal(c.Rule, &rule); err != nil {
			continue
		}
		if err := RefuseCacheRules([]CacheRule{rule}); err == nil {
			t.Errorf("a rule with %s validated: %s", c.Reason, c.Rule)
		}
	}
}

func TestCacheRulesTravelToAnOriginAsOneJSONVariable(t *testing.T) {
	t.Parallel()

	rules := []CacheRule{{Path: "/blog/*", Revalidate: 60, Expire: 600}}
	if got, want := CacheRulesVariable(rules), `[{"path":"/blog/*","revalidate":60,"expire":600}]`; got != want {
		t.Errorf("CacheRulesVariable = %q, want %q", got, want)
	}
	if got := CacheRulesVariable(nil); got != "" {
		t.Errorf("CacheRulesVariable(nil) = %q, want empty", got)
	}
}
