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

func TestNormalizeRFC3339IsUTCToTheSecondAndEmptyForWhatIsNotRFC3339(t *testing.T) {
	for raw, want := range map[string]string{
		"2026-10-05T07:00:00.123Z":      "2026-10-05T07:00:00Z",
		"2026-10-05T10:00:00+03:00":     "2026-10-05T07:00:00Z",
		"2026-10-05T07:00:00Z":          "2026-10-05T07:00:00Z",
		"":                              "",
		"yesterday":                     "",
		"Mon, 05 Oct 2026 07:00:00 GMT": "",
	} {
		if got := NormalizeRFC3339(raw); got != want {
			t.Errorf("NormalizeRFC3339(%q) = %q, want %q", raw, got, want)
		}
	}
}
