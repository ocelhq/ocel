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

func TestPhaseIDIsTheGoldenDigestOfTheUnitAndPhasePair(t *testing.T) {
	for _, tc := range []struct {
		unit, phase string
		want        string
	}{
		{UnitEnvironment, PhaseBuilding, "4b5ac07b8124802c"},
		{UnitEnvironment, PhaseUploading, "8b528c00a0fb6065"},
		{UnitEnvironment, PhaseProvisioning, "ed0ca2aae3a67905"},
		{"production--infra", PhaseProvisioning, "a97c45b1597f53f3"},
		{"production--web--r1", PhaseProvisioning, "43cc72e38cfddef8"},
		{UnitPromotion, PhaseFinalizing, "510408c02b4d8fe2"},
		{UnitEdge, PhaseDeleting, "e5831158a1cb8675"},
	} {
		if got := hex.EncodeToString(PhaseID(tc.unit, tc.phase)); got != tc.want {
			t.Errorf("PhaseID(%q, %q) = %s, want %s", tc.unit, tc.phase, got, tc.want)
		}
	}
}

func TestStageIDsAreEightBytesAndUnambiguousAcrossFieldBoundaries(t *testing.T) {
	t.Run("every id is eight bytes", func(t *testing.T) {
		if n := len(UnitID(UnitEnvironment)); n != StageIDLen {
			t.Errorf("UnitID length = %d, want %d", n, StageIDLen)
		}
		if n := len(PhaseID(UnitEnvironment, PhaseBuilding)); n != StageIDLen {
			t.Errorf("PhaseID length = %d, want %d", n, StageIDLen)
		}
	})

	t.Run("a split of the same characters is a different id", func(t *testing.T) {
		if hex.EncodeToString(PhaseID("ab", "c")) == hex.EncodeToString(PhaseID("a", "bc")) {
			t.Error(`PhaseID("ab", "c") collides with PhaseID("a", "bc")`)
		}
		if hex.EncodeToString(UnitID("a")) == hex.EncodeToString(PhaseID("a", "")) {
			t.Error(`UnitID("a") collides with PhaseID("a", "")`)
		}
	})
}
