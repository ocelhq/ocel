package cron_test

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/cron"
)

func at(t *testing.T, text string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestAScheduleFiresAtTheNextMinuteItMatchesAfterTheGivenTime(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		expr, after, want string
	}{
		{"*/15 * * * *", "2026-10-01T10:07:30Z", "2026-10-01T10:15:00Z"},
		{"*/15 * * * *", "2026-10-01T10:15:00Z", "2026-10-01T10:30:00Z"},
		{"0 3 * * *", "2026-10-01T03:00:00Z", "2026-10-02T03:00:00Z"},
		{"0 9-17 * * MON-FRI", "2026-10-02T17:30:00Z", "2026-10-05T09:00:00Z"},
		{"30 2 1,15 * *", "2026-10-02T00:00:00Z", "2026-10-15T02:30:00Z"},
		{"0 0 1 JAN *", "2026-10-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"0 12 * * 7", "2026-10-01T00:00:00Z", "2026-10-04T12:00:00Z"},
		{"0 12 * * 0", "2026-10-01T00:00:00Z", "2026-10-04T12:00:00Z"},
		{"0 0 13 * FRI", "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z"},
		{"0 0 29 2 *", "2026-10-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"5/20 * * * *", "2026-10-01T10:26:00Z", "2026-10-01T10:45:00Z"},
	} {
		schedule, err := cron.Parse(tc.expr)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", tc.expr, err)
		}
		if got := schedule.Next(at(t, tc.after)); !got.Equal(at(t, tc.want)) {
			t.Errorf("Parse(%q).Next(%s) = %s, want %s", tc.expr, tc.after, got.Format(time.RFC3339), tc.want)
		}
	}
}

func TestAScheduleReadsTheClockInUTC(t *testing.T) {
	t.Parallel()

	schedule, err := cron.Parse("0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	nairobi := time.FixedZone("EAT", 3*60*60)
	got := schedule.Next(time.Date(2026, 10, 1, 1, 0, 0, 0, nairobi))
	if want := at(t, "2026-10-01T03:00:00Z"); !got.Equal(want) {
		t.Errorf("Next = %s, want %s", got, want)
	}
}

func TestAnExpressionThatNamesNoScheduleIsRefused(t *testing.T) {
	t.Parallel()

	for _, expr := range []string{"* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *", "* * * * * *", "0 0 31 2 *"} {
		if _, err := cron.Parse(expr); err == nil {
			t.Errorf("Parse(%q) accepted it", expr)
		}
	}
}
