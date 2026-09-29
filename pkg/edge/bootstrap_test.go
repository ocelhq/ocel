package edge

import "testing"

func TestAnEdgeGroupIsNamedForItsEdgeAndTheOriginWhenThereIsNone(t *testing.T) {
	t.Parallel()

	if got := EdgeGroupName(sampleKind); got != "sample/edge" {
		t.Errorf("EdgeGroupName(sample) = %q, want sample/edge", got)
	}
	if kind, ok := EdgeGroupKindOf(EdgeGroupName(sampleKind)); !ok || kind != sampleKind {
		t.Errorf("EdgeGroupKindOf(EdgeGroupName(sample)) = %q, %v, want sample back", kind, ok)
	}
	if got := EdgeGroupName(None); got != "origin" {
		t.Errorf("EdgeGroupName(no edge) = %q, want origin: nothing else answers a hostname when no edge is in front", got)
	}
	if kind, ok := EdgeGroupKindOf(EdgeGroupName(None)); ok {
		t.Errorf("EdgeGroupKindOf(origin) = %q, want no edge kind read off the origin's group", kind)
	}
}
