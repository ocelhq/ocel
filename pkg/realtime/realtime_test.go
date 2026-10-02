package realtime

import (
	"strings"
	"testing"
	"time"
)

func TestATokenTTLFromTenSecondsToFiveMinutesIsAccepted(t *testing.T) {
	t.Parallel()

	for _, ttl := range []time.Duration{10 * time.Second, 60 * time.Second, 300 * time.Second} {
		if err := refuseTokenTTL(ttl); err != nil {
			t.Errorf("refuseTokenTTL(%s) = %v, want it accepted", ttl, err)
		}
	}
}

func TestATokenTTLOutsideTenSecondsToFiveMinutesIsRefused(t *testing.T) {
	t.Parallel()

	for _, ttl := range []time.Duration{0, 9*time.Second + 999*time.Millisecond, 300*time.Second + time.Millisecond, time.Hour, -time.Minute} {
		err := refuseTokenTTL(ttl)
		if err == nil || !strings.Contains(err.Error(), "10s") || !strings.Contains(err.Error(), "5m0s") {
			t.Errorf("refuseTokenTTL(%s) = %v, want it refused naming the 10s to 5m0s range", ttl, err)
		}
	}
}

func TestANamespaceIsALegalChannelSegment(t *testing.T) {
	t.Parallel()

	if err := refuseNamespace(strings.Repeat("a", 50)); err != nil {
		t.Errorf("refuseNamespace(50 characters) = %v, want it accepted", err)
	}
	if err := refuseNamespace(strings.Repeat("a", 51)); err == nil || !strings.Contains(err.Error(), "50") {
		t.Errorf("refuseNamespace(51 characters) = %v, want it refused naming the 50-character limit", err)
	}
}
