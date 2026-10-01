package kvstore

import (
	"strings"
	"testing"
)

func TestMemoryIsReadInTheUnitsValkeyReadsIt(t *testing.T) {
	t.Parallel()

	for written, bytes := range map[string]int64{
		"256mb":   256 << 20,
		"256MB":   256 << 20,
		"32mb":    32 << 20,
		"1gb":     1 << 30,
		"32gb":    32 << 30,
		"65536kb": 64 << 20,
	} {
		got, err := ParseMemory(written)
		if err != nil || got != bytes {
			t.Errorf("ParseMemory(%q) = %d, %v, want %d", written, got, err, bytes)
		}
	}
}

func TestTheDefaultMemoryIsWithinTheBounds(t *testing.T) {
	t.Parallel()

	got, err := ParseMemory(DefaultMemory)
	if err != nil || got != 256<<20 {
		t.Errorf("ParseMemory(DefaultMemory) = %d, %v, want 256 MiB", got, err)
	}
}

func TestMemoryThatIsNotAWholeSizeIsRefused(t *testing.T) {
	t.Parallel()

	for _, written := range []string{"", "256", "1.5gb", "-1mb", "mb", "256 mb", "256mib", "1tb"} {
		if _, err := ParseMemory(written); err == nil || !strings.Contains(err.Error(), "kb, mb or gb") {
			t.Errorf("ParseMemory(%q) = %v, want it refused naming the units a size is written in", written, err)
		}
	}
}

func TestMemoryOutsideTheBoundsIsRefusedNamingThem(t *testing.T) {
	t.Parallel()

	for _, written := range []string{"0mb", "16mb", "31mb", "33gb", "1024gb"} {
		_, err := ParseMemory(written)
		if err == nil || !strings.Contains(err.Error(), "32mb") || !strings.Contains(err.Error(), "32gb") {
			t.Errorf("ParseMemory(%q) = %v, want it refused naming the bounds 32mb and 32gb", written, err)
		}
	}
}
