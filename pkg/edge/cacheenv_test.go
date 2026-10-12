package edge

import (
	"reflect"
	"testing"
)

func TestAnOriginBehindACachingEdgeIsToldWhatItNeedsToShapeItsResponses(t *testing.T) {
	t.Parallel()

	got := NewCachingOriginEnv(Facts{CachesResponses: true, InvalidatesByCacheTag: true}, "r0a1b2c3d")
	want := map[string]string{
		CacheTagPurgeVar:   "1",
		OriginFreshOnlyVar: "1",
		ReleaseTokenVar:    "r0a1b2c3d",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

func TestAnOriginBehindAnEdgeThatRunsCodeIsToldTheEdgeAsksForCaching(t *testing.T) {
	t.Parallel()

	got := NewCachingOriginEnv(Facts{CachesResponses: true, RunsCode: true}, "r0a1b2c3d")
	if got[EdgeAsksToCacheVar] != "1" {
		t.Errorf("%s = %q, want 1: only the gateway's own requests are to be shaped for its cache", EdgeAsksToCacheVar, got[EdgeAsksToCacheVar])
	}
}

func TestAnOriginBehindNoCachingEdgeIsToldNothing(t *testing.T) {
	t.Parallel()

	if got := NewCachingOriginEnv(Facts{RunsCode: true}, "r0a1b2c3d"); len(got) != 0 {
		t.Errorf("env = %v, want none: nothing in front of the origin caches", got)
	}
}
