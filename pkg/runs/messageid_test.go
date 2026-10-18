package runs_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/runs"
)

var ulid = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

func TestAMessageIDIsACrockfordULIDThatSortsByTime(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	earlier, later := runs.NewMessageID(at), runs.NewMessageID(at.Add(time.Millisecond))
	if !ulid.MatchString(earlier) || !ulid.MatchString(later) {
		t.Fatalf("ids %q and %q, want 26-character Crockford ULIDs", earlier, later)
	}
	if earlier[:10] >= later[:10] {
		t.Errorf("the id a millisecond later, %q, does not sort after %q", later, earlier)
	}
	if got := runs.NewMessageID(time.UnixMilli(0x0123456789AB)); got[:10] != "014D2PF2DB" {
		t.Errorf("the time part of %q is not the 48-bit millisecond count in Crockford base32", got)
	}
	if first, second := runs.NewMessageID(at), runs.NewMessageID(at); first == second {
		t.Error("two ids in one millisecond are equal")
	}
}

func TestAMessageIDDrawnFromASeedIsTheSameEachTimeAndApartForEachSeed(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first, again := runs.MessageIDFrom(at, "pubsub-17"), runs.MessageIDFrom(at, "pubsub-17")
	if !ulid.MatchString(first) {
		t.Fatalf("id %q, want a 26-character Crockford ULID", first)
	}
	if first != again {
		t.Errorf("one seed drew %q and then %q, want a redelivered message to keep its id", first, again)
	}
	if other := runs.MessageIDFrom(at, "pubsub-18"); other == first {
		t.Errorf("two seeds both drew %q", first)
	}
	if first[:10] != runs.NewMessageID(at)[:10] {
		t.Errorf("the time part of %q is not the time it was drawn at", first)
	}
}
