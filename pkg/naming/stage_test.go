package naming

import (
	"encoding/hex"
	"testing"
)

func TestUnitIDIsTheGoldenDigestOfTheCanonicalName(t *testing.T) {
	for _, tc := range []struct {
		unit string
		want string
	}{
		{UnitEnvironment, "9f2ecbbdfa2db89d"},
		{UnitEdge, "000c0a32c587a5a8"},
		{UnitPromotion, "17505ced11f71bd3"},
		{"production--infra", "84736c1d8e8b6ba5"},
		{"production--web--r1", "365f31c02650f8b4"},
	} {
		if got := hex.EncodeToString(UnitID(tc.unit)); got != tc.want {
			t.Errorf("UnitID(%q) = %s, want %s", tc.unit, got, tc.want)
		}
	}
}

func TestStageIDsAreEightBytesAndUnambiguousAcrossFieldBoundaries(t *testing.T) {
	t.Run("every id is eight bytes", func(t *testing.T) {
		if n := len(UnitID(UnitEnvironment)); n != StageIDLen {
			t.Errorf("UnitID length = %d, want %d", n, StageIDLen)
		}
	})

	t.Run("a name that only differs in length is a different id", func(t *testing.T) {
		if hex.EncodeToString(UnitID("a")) == hex.EncodeToString(UnitID("a\x00")) {
			t.Error(`UnitID("a") collides with UnitID("a\x00")`)
		}
	})
}
