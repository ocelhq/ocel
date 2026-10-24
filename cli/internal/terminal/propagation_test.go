package terminal

import (
	"testing"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestPropagationNote(t *testing.T) {
	cases := []struct {
		name  string
		bound *progressv1.Propagation
		want  string
	}{
		{name: "an unrecorded bound says nothing"},
		{name: "an instant pointer move says nothing", bound: &progressv1.Propagation{}},
		{
			name:  "a published bound promises the duration",
			bound: &progressv1.Propagation{TypicalMs: 5000, Published: true},
			want:  "propagates within 5s",
		},
		{
			name:  "an unpublished bound approximates the duration",
			bound: &progressv1.Propagation{TypicalMs: 5000},
			want:  "propagates in ~5s",
		},
		{
			name:  "the duration comes from the recorded milliseconds",
			bound: &progressv1.Propagation{TypicalMs: 90000, Published: true},
			want:  "propagates within 90s",
		},
		{
			name:  "a fractional second keeps its fraction",
			bound: &progressv1.Propagation{TypicalMs: 1500},
			want:  "propagates in ~1.5s",
		},
		{
			name:  "a sub-second bound stays in milliseconds",
			bound: &progressv1.Propagation{TypicalMs: 250, Published: true},
			want:  "propagates within 250ms",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PropagationNote(tc.bound); got != tc.want {
				t.Errorf("PropagationNote(%v) = %q, want %q", tc.bound, got, tc.want)
			}
		})
	}
}
