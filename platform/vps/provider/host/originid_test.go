package host

import (
	"context"
	"testing"
)

func TestABoxMintsItsOriginIDOnceAndHandsTheSameOneBack(t *testing.T) {
	t.Parallel()
	box := claimingBox(t, routed())

	first, err := box.host().Routing().EnsureOriginID(context.Background())
	if err != nil {
		t.Fatalf("EnsureOriginID() = %v", err)
	}
	again, err := box.host().Routing().EnsureOriginID(context.Background())
	if err != nil {
		t.Fatalf("EnsureOriginID() again = %v", err)
	}

	if first == "" || again != first {
		t.Errorf("EnsureOriginID() = %q then %q, want one id for the box: every Cloudflare resource this box's origin owns is named for it", first, again)
	}
	if recorded := readBack(t, box).OriginID; recorded != first {
		t.Errorf("the routing table records origin id %q, want %q", recorded, first)
	}
}

func TestABoxKeepsTheOriginIDItAlreadyHolds(t *testing.T) {
	t.Parallel()
	state := routed()
	state.OriginID = "k3j9x0"
	box := claimingBox(t, state)

	held, err := box.host().Routing().EnsureOriginID(context.Background())

	if err != nil || held != "k3j9x0" {
		t.Errorf("EnsureOriginID() = %q, %v, want the id the box already holds", held, err)
	}
}
