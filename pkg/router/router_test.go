package router

import "testing"

func TestAPointerNamedNothingIsTheProductionPointer(t *testing.T) {
	t.Parallel()

	for pointer, want := range map[string]string{"": "@production", "@production": "@production", "pr-42": "pr-42"} {
		if got := ResolvePointer(pointer); got != want {
			t.Errorf("ResolvePointer(%q) = %q, want %q", pointer, got, want)
		}
		if got := IsDefaultPointer(pointer); got != (want == "@production") {
			t.Errorf("IsDefaultPointer(%q) = %v, want %v", pointer, got, want == "@production")
		}
	}
}
