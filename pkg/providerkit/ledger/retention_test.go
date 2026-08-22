package ledger_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
)

func id(name string) string { return name }

func TestRetainKeepsTheNewestNAndCollectsTheRest(t *testing.T) {
	t.Parallel()

	kept, removed := ledger.Retain([]string{"p4", "p3", "p2", "p1"}, 2, "p4", id)
	if !slices.Equal(kept, []string{"p4", "p3"}) {
		t.Errorf("Retain() kept %v, want the newest two", kept)
	}
	if !slices.Equal(removed, []string{"p2", "p1"}) {
		t.Errorf("Retain() collected %v, want everything past the window", removed)
	}
}

func TestRetainNeverCollectsWhatIsServingNow(t *testing.T) {
	t.Parallel()

	kept, removed := ledger.Retain([]string{"p4", "p3", "p2", "p1"}, 1, "p2", id)
	if !slices.Contains(kept, "p2") {
		t.Fatalf("Retain() kept %v, want the promotion the pointer serves among them however old it is", kept)
	}
	if slices.Contains(removed, "p2") {
		t.Error("Retain() collected the promotion serving traffic, and nothing would answer the moment it went")
	}
}

func TestRetainKeepingNoneStillKeepsWhatIsServing(t *testing.T) {
	t.Parallel()

	kept, removed := ledger.Retain([]string{"p2", "p1"}, 0, "p1", id)
	if !slices.Equal(kept, []string{"p1"}) {
		t.Errorf("Retain() kept %v, want only the active promotion", kept)
	}
	if !slices.Equal(removed, []string{"p2"}) {
		t.Errorf("Retain() collected %v, want everything else", removed)
	}
}

func TestRetainOverNothingCollectsNothing(t *testing.T) {
	t.Parallel()

	kept, removed := ledger.Retain([]string(nil), 3, "", id)
	if len(kept) != 0 || len(removed) != 0 {
		t.Errorf("Retain() over an empty history = %v / %v, want nothing either way", kept, removed)
	}
}
