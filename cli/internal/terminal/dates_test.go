package terminal

import "testing"

func TestEpochRFC3339IsUTCAndEmptyForTheZeroEpoch(t *testing.T) {
	if got := EpochRFC3339(1757000000); got != "2025-09-04T15:33:20Z" {
		t.Errorf("EpochRFC3339 = %q, want the UTC RFC 3339 time", got)
	}
	if got := EpochRFC3339(0); got != "" {
		t.Errorf("EpochRFC3339(0) = %q, want empty", got)
	}
}
