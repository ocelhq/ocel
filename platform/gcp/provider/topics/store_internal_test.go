package topics

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

func TestEveryRunIsPurgedThirtyDaysAfterItFinishedOrElseAfterItWasDue(t *testing.T) {
	created := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		run  runs.Run
		want time.Time
	}{
		"finished": {
			run:  runs.Run{Status: provider.RunCompleted, CreatedAt: created, DueAt: created, FinishedAt: created.Add(time.Hour)},
			want: created.Add(time.Hour + RunRetention),
		},
		"queued": {
			run:  runs.Run{Status: provider.RunQueued, CreatedAt: created, DueAt: created},
			want: created.Add(RunRetention),
		},
		"delayed": {
			run:  runs.Run{Status: provider.RunDelayed, CreatedAt: created, DueAt: created.Add(48 * time.Hour)},
			want: created.Add(48*time.Hour + RunRetention),
		},
		"executing past its due time": {
			run:  runs.Run{Status: provider.RunExecuting, CreatedAt: created.Add(time.Minute), DueAt: created, StartedAt: created.Add(2 * time.Minute)},
			want: created.Add(time.Minute + RunRetention),
		},
	} {
		if got := documentOf(tc.run, "r").PurgeAt; !got.Equal(tc.want) {
			t.Errorf("a %s run is purged at %v, want %v", name, got, tc.want)
		}
	}
}
