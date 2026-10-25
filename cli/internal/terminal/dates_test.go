package terminal

import (
	"testing"
	"time"
)

func TestEpochRFC3339IsUTCAndEmptyForTheZeroEpoch(t *testing.T) {
	if got := EpochRFC3339(1757000000); got != "2025-09-04T15:33:20Z" {
		t.Errorf("EpochRFC3339 = %q, want the UTC RFC 3339 time", got)
	}
	if got := EpochRFC3339(0); got != "" {
		t.Errorf("EpochRFC3339(0) = %q, want empty", got)
	}
}

func TestFormatRFC3339IsUTCToTheSecondAndEmptyForNoTime(t *testing.T) {
	at := time.Date(2026, 10, 5, 10, 0, 0, 123_000_000, time.FixedZone("EAT", 3*60*60))
	if got := FormatRFC3339(&at); got != "2026-10-05T07:00:00Z" {
		t.Errorf("FormatRFC3339 = %q, want the UTC RFC 3339 time to the second", got)
	}
	if got := FormatRFC3339(nil); got != "" {
		t.Errorf("FormatRFC3339(nil) = %q, want empty", got)
	}
}

func TestAgeSaysHowLongAgoInTheLargestWholeUnitUpToAMonth(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{now.Add(-20 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-12 * 24 * time.Hour), "12d ago"},
		{now.Add(-40 * 24 * time.Hour), "2026-09-15"},
		{now.Add(time.Hour), "2026-10-25"},
	} {
		if got := Age(tc.at, now); got != tc.want {
			t.Errorf("Age(%s) = %q, want %q", now.Sub(tc.at), got, tc.want)
		}
	}
}

func TestEpochAgeIsRelativeOnATerminalAndADateOffOne(t *testing.T) {
	twoHoursAgo := time.Now().Add(-2*time.Hour - time.Minute).Unix()
	if got := (Presentation{TTY: true}).EpochAge(twoHoursAgo); got != "2h ago" {
		t.Errorf("EpochAge on a terminal = %q, want 2h ago", got)
	}
	if got, want := (Presentation{}).EpochAge(twoHoursAgo), EpochDate(twoHoursAgo); got != want {
		t.Errorf("EpochAge off a terminal = %q, want the date %q", got, want)
	}
	if got := (Presentation{TTY: true}).EpochAge(0); got != NoValue {
		t.Errorf("EpochAge(0) = %q, want %q", got, NoValue)
	}
}

func TestAbbreviateIDKeepsTheFirstEightCharactersOfALongID(t *testing.T) {
	if got := AbbreviateID("91532c18c75ea0563b5514d396a4ae2a"); got != "91532c18" {
		t.Errorf("AbbreviateID = %q, want 91532c18", got)
	}
	if got := AbbreviateID("pr-12"); got != "pr-12" {
		t.Errorf("AbbreviateID of a short id = %q, want it whole", got)
	}
}
