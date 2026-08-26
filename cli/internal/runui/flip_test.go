package runui

import (
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestFlipNote(t *testing.T) {
	cases := []struct {
		name  string
		bound *progressv1.FlipBound
		want  string
	}{
		{name: "an unrecorded bound says nothing"},
		{name: "an instant flip says nothing", bound: &progressv1.FlipBound{}},
		{
			name:  "a published bound promises the duration",
			bound: &progressv1.FlipBound{TypicalMs: 5000, Published: true},
			want:  "propagates within ~5 s",
		},
		{
			name:  "an unpublished bound qualifies the duration",
			bound: &progressv1.FlipBound{TypicalMs: 5000},
			want:  "propagates in ~5 s (typical, not guaranteed)",
		},
		{
			name:  "the duration comes from the recorded milliseconds",
			bound: &progressv1.FlipBound{TypicalMs: 90000, Published: true},
			want:  "propagates within ~90 s",
		},
		{
			name:  "a fractional second keeps its fraction",
			bound: &progressv1.FlipBound{TypicalMs: 1500},
			want:  "propagates in ~1.5 s (typical, not guaranteed)",
		},
		{
			name:  "a sub-second bound stays in milliseconds",
			bound: &progressv1.FlipBound{TypicalMs: 250, Published: true},
			want:  "propagates within ~250 ms",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FlipNote(tc.bound); got != tc.want {
				t.Errorf("FlipNote(%v) = %q, want %q", tc.bound, got, tc.want)
			}
		})
	}
}
