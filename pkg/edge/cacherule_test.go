package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type cacheRuleFixture struct {
	Rules   []CacheRule `json:"rules"`
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
	if got, want := EncodeCacheRules(rules), `[{"path":"/blog/*","revalidate":60,"expire":600}]`; got != want {
		t.Errorf("EncodeCacheRules = %q, want %q", got, want)
	}
	if got := EncodeCacheRules(nil); got != "" {
		t.Errorf("EncodeCacheRules(nil) = %q, want empty", got)
	}
}
