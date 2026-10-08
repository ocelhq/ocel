package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAStaticPathIsImmutableOnlyUnderAnImmutablePrefixAndOutsideEveryMustRevalidatePrefix(t *testing.T) {
	t.Parallel()

	static := &Static{
		ImmutablePrefixes:      []string{"/docs/_next/static/"},
		MustRevalidatePrefixes: []string{"/docs/_next/static/service-worker/"},
	}
	for path, want := range map[string]bool{
		"/docs/_next/static/chunks/main.js":       true,
		"/docs/_next/static/service-worker/sw.js": false,
		"/_next/static/chunks/main.js":            false,
		"/docs/favicon.ico":                       false,
	} {
		if got := static.IsImmutable(path); got != want {
			t.Errorf("IsImmutable(%q) = %v, want %v", path, got, want)
		}
	}
	if (*Static)(nil).IsImmutable("/docs/_next/static/chunks/main.js") {
		t.Error("a build with no static dir calls a path immutable")
	}
}

func TestTheStaticRulesClassifyEveryPathAsTheEdgeContractFixtureSays(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "edge", "contract", "fixtures", "static-rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Static Static `json:"static"`
		Cases  []struct {
			Path      string `json:"path"`
			Immutable bool   `json:"immutable"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		want := RevalidateCacheControl
		if c.Immutable {
			want = ImmutableCacheControl
		}
		if got := fixture.Static.CacheControl(c.Path); got != want {
			t.Errorf("CacheControl(%q) = %q, want %q", c.Path, got, want)
		}
	}
}
