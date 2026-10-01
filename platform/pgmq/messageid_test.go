package pgmq

import (
	"regexp"
	"testing"
	"time"
)

var ulid = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

func TestAMessageIDIsACrockfordULIDThatSortsByTime(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	earlier, later := newMessageID(at), newMessageID(at.Add(time.Millisecond))
	if !ulid.MatchString(earlier) || !ulid.MatchString(later) {
		t.Fatalf("ids %q and %q, want 26-character Crockford ULIDs", earlier, later)
	}
	if earlier[:10] >= later[:10] {
		t.Errorf("the id a millisecond later, %q, does not sort after %q", later, earlier)
	}
	if got := newMessageID(time.UnixMilli(0x0123456789AB)); got[:10] != "014D2PF2DB" {
		t.Errorf("the time part of %q is not the 48-bit millisecond count in Crockford base32", got)
	}
	if first, second := newMessageID(at), newMessageID(at); first == second {
		t.Error("two ids in one millisecond are equal")
	}
}
