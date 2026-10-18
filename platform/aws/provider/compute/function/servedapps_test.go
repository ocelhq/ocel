package function

import (
	"sync"
	"testing"
)

func TestServedAppsHandsOutASnapshotRatherThanTheEntryItself(t *testing.T) {
	t.Parallel()

	served := NewServedApps()
	served.Record(nil, "web", []string{"fn--web--entry"}, nil)
	served.Realize("fn--web--entry", "shop-prod-web-entry")

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			served.warmed("shop-prod-web-entry", warmReply{Key: "a-cache-key"})
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn, known := served.Find("shop-prod-web-entry")
			if !known {
				t.Error("Find() lost the root function it was told about")
				return
			}
			fn.Warmed.Key = "a reader's own scribble"
		}()
	}
	wg.Wait()

	fn, known := served.Find("shop-prod-web-entry")
	if !known {
		t.Fatal("Find() lost the root function it was told about")
	}
	if fn.Warmed.Key != "a-cache-key" {
		t.Errorf("the index stores %q, want the warm reply it recorded: a caller may not write through what it was handed", fn.Warmed.Key)
	}
}
