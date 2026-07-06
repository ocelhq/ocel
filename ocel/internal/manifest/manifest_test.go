package manifest

import (
	"sync"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestManifest_AddAndSnapshot(t *testing.T) {
	m := New()

	m.Add(Entry{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES})

	got := m.Snapshot()
	want := []Entry{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Snapshot() = %+v, want %+v", got, want)
	}
}

func TestManifest_SnapshotIsIndependentCopy(t *testing.T) {
	m := New()
	m.Add(Entry{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES})

	snap := m.Snapshot()
	snap[0].Name = "mutated"

	got := m.Snapshot()
	if got[0].Name != "main" {
		t.Fatalf("mutating a snapshot affected the manifest: got %q", got[0].Name)
	}
}

func TestManifest_ConcurrentAdd(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.Add(Entry{Name: "r", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES})
		}(i)
	}
	wg.Wait()

	if got := len(m.Snapshot()); got != 50 {
		t.Fatalf("Snapshot() len = %d, want 50", got)
	}
}
