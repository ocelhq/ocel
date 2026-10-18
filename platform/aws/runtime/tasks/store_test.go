package tasks

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/runs"
)

func TestEveryRunIsPurgedThirtyDaysAfterItFinishesOrElseAfterItWasDueOrCreated(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		run  runs.Run
		want time.Time
	}{
		"finished":             {runs.Run{CreatedAt: created, DueAt: created, FinishedAt: created.Add(time.Hour)}, created.Add(time.Hour + runRetention)},
		"due later, unstarted": {runs.Run{CreatedAt: created, DueAt: created.Add(48 * time.Hour)}, created.Add(48*time.Hour + runRetention)},
		"due now, stranded":    {runs.Run{CreatedAt: created, DueAt: created, StartedAt: created}, created.Add(runRetention)},
	} {
		item := Store{prefix: "p#"}.itemOf(tc.run, "r1")
		if item.ExpiresAtUnix != tc.want.Unix() {
			t.Errorf("%s: purged at %v, want %v", name, time.Unix(item.ExpiresAtUnix, 0).UTC(), tc.want)
		}
	}
}
