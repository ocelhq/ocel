package edge

import (
	"slices"
	"testing"
	"time"
)

func TestDeclaredCapabilities(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind  Kind
		needs []Need
		flip  FlipBound
	}{
		{
			kind:  KindCloudflare,
			needs: AllNeeds(),
			flip:  FlipBound{},
		},
		{
			kind:  KindNative,
			needs: []Need{NeedEdgeCache, NeedStreaming},
			flip:  FlipBound{Typical: 5 * time.Second},
		},
		{
			kind:  KindNone,
			needs: []Need{NeedStreaming},
			flip:  FlipBound{Typical: 5 * time.Second},
		},
	} {
		t.Run(string(tc.kind)+" answers from the declaration alone", func(t *testing.T) {
			t.Parallel()

			caps := CapabilitiesOf(tc.kind)
			if got := caps.Supported(); !slices.Equal(got, tc.needs) {
				t.Errorf("Supported() = %v, want %v", got, tc.needs)
			}
			for _, need := range AllNeeds() {
				if want := slices.Contains(tc.needs, need); caps.Supports(need) != want {
					t.Errorf("Supports(%q) = %v, want %v", need, caps.Supports(need), want)
				}
			}
			if got := caps.FlipBound(); got != tc.flip {
				t.Errorf("FlipBound() = %+v, want %+v", got, tc.flip)
			}
		})
	}

	t.Run("a kind nothing declares supports nothing", func(t *testing.T) {
		t.Parallel()

		caps := CapabilitiesOf(Kind("nonsense"))
		if got := caps.Supported(); len(got) != 0 {
			t.Errorf("Supported() = %v, want nothing", got)
		}
		if got := caps.FlipBound(); got != (FlipBound{}) {
			t.Errorf("FlipBound() = %+v, want the zero bound", got)
		}
	})
}
