package providerserver

import (
	"encoding/hex"
	"testing"
)

func TestARootSpanIDIsTheGoldenDigestOfItsName(t *testing.T) {
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
		if id := RootSpanID(tc.name); hex.EncodeToString(id[:]) != tc.want {
			t.Errorf("RootSpanID(%q) = %x, want %s", tc.name, id, tc.want)
		}
	}
}

func TestTwoNamesThatDifferOnlyInATrailingZeroByteAreTwoRootSpanIDs(t *testing.T) {
	if RootSpanID("a") == RootSpanID("a\x00") {
		t.Error(`RootSpanID("a") collides with RootSpanID("a\x00")`)
	}
}
