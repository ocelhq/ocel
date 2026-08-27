package deploy

import (
	"sync"
	"testing"
)

func TestServedAppsHandsOutASnapshotRatherThanTheEntryItself(t *testing.T) {
	t.Parallel()

	served := newServedApps()
	served.plan(nil, "web", []string{"fn--web--entry"}, nil)
	served.realized("fn--web--entry", "shop-prod-web-entry")

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
			held, known := served.byPhysicalName("shop-prod-web-entry")
			if !known {
				t.Error("byPhysicalName() lost the entry function it was told about")
				return
			}
			held.Warmed.Key = "a reader's own scribble"
		}()
	}
	wg.Wait()

	held, known := served.byPhysicalName("shop-prod-web-entry")
	if !known {
		t.Fatal("byPhysicalName() lost the entry function it was told about")
	}
	if held.Warmed.Key != "a-cache-key" {
		t.Errorf("the index holds %q, want the warm reply it recorded: a caller may not write through what it was handed", held.Warmed.Key)
	}
}
