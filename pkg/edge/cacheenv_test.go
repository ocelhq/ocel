package edge

import (
	"reflect"
	"testing"
)

func TestAnOriginBehindACachingEdgeIsToldWhatItNeedsToShapeItsResponses(t *testing.T) {
	t.Parallel()

	got := NewCachingOriginEnv(Facts{CachesResponses: true, InvalidatesByCacheTag: true}, "r0a1b2c3d")
	want := map[string]string{
		CacheTagPurgeVar: "1",
		EdgeCacheVar:     string(EdgeCacheAll),
		ReleaseTokenVar:  "r0a1b2c3d",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

func TestAnOriginIsToldWhichResponsesItsEdgeCaches(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		facts Facts
		want  EdgeCacheMode
	}{
		"an edge that stores what the response headers allow":     {Facts{CachesResponses: true}, EdgeCacheAll},
		"an edge that runs code and stores only what it asks for": {Facts{CachesResponses: true, RunsCode: true}, EdgeCacheMarked},
		"an edge that runs code and stores nothing":               {Facts{RunsCode: true}, EdgeCacheNone},
		"no edge": {Facts{}, EdgeCacheNone},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := NewEdgeCacheMode(test.facts); got != test.want {
				t.Errorf("NewEdgeCacheMode(%+v) = %q, want %q", test.facts, got, test.want)
			}
		})
	}
}

func TestAnOriginIsToldTheCacheControlHeaderItsEdgeReads(t *testing.T) {
	t.Parallel()

	got := NewCachingOriginEnv(Facts{CachesResponses: true, RunsCode: true, CacheControlHeader: "cloudflare-cdn-cache-control"}, "r0a1b2c3d")
	want := map[string]string{
		EdgeCacheVar:              string(EdgeCacheMarked),
		EdgeCacheControlHeaderVar: "cloudflare-cdn-cache-control",
		ReleaseTokenVar:           "r0a1b2c3d",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

func TestAnOriginBehindNoCachingEdgeIsToldNothing(t *testing.T) {
	t.Parallel()

	if got := NewCachingOriginEnv(Facts{RunsCode: true}, "r0a1b2c3d"); len(got) != 0 {
		t.Errorf("env = %v, want none: nothing in front of the origin caches", got)
	}
}
