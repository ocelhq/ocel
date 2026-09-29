package naming

import (
	"encoding/hex"
	"testing"
)

func TestSpanIDIsTheGoldenDigestOfTheCanonicalName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{SpanEnvironment, "9f2ecbbdfa2db89d"},
		{SpanEdge, "000c0a32c587a5a8"},
		{SpanPromotion, "17505ced11f71bd3"},
		{"production--infra", "84736c1d8e8b6ba5"},
		{"production--web--r1", "365f31c02650f8b4"},
	} {
		if got := hex.EncodeToString(SpanID(tc.name)); got != tc.want {
			t.Errorf("SpanID(%q) = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestSpanIDsAreEightBytesAndUnambiguousAcrossFieldBoundaries(t *testing.T) {
	t.Run("every id is eight bytes", func(t *testing.T) {
		if n := len(SpanID(SpanEnvironment)); n != SpanIDLen {
			t.Errorf("SpanID length = %d, want %d", n, SpanIDLen)
		}
	})

	t.Run("a name that only differs in length is a different id", func(t *testing.T) {
		if hex.EncodeToString(SpanID("a")) == hex.EncodeToString(SpanID("a\x00")) {
			t.Error(`SpanID("a") collides with SpanID("a\x00")`)
		}
	})
}
