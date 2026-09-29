package progress

import (
	"strings"
	"testing"
)

func TestASpanNameLosesItsControlCharactersIsCappedAndIsNeverEmpty(t *testing.T) {
	t.Parallel()

	if got := SanitizeSpanName("\x1b[2J deploy\n"); got != "[2J deploy" {
		t.Errorf("SanitizeSpanName() = %q, want the control characters gone", got)
	}
	if got := SanitizeSpanName(" \t "); got != "span" {
		t.Errorf("SanitizeSpanName() = %q, want a fallback name", got)
	}
	if got := SanitizeSpanName(strings.Repeat("a", MaxSpanNameLen*2)); len(got) != MaxSpanNameLen {
		t.Errorf("SanitizeSpanName() is %d long, want it capped at %d", len(got), MaxSpanNameLen)
	}
}

func TestAMessageLosesItsControlCharactersButNotItsLength(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", MaxSpanNameLen*2)
	if got := SanitizeMessage("\x07" + long); got != long {
		t.Errorf("SanitizeMessage() is %d long, want the %d characters that were not control characters", len(got), len(long))
	}
}
