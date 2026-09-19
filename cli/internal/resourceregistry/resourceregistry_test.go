package resourceregistry

import (
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declare"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestRegistry(t *testing.T) {
	t.Parallel()

	t.Run("add and snapshot", func(t *testing.T) {
		t.Parallel()

		m := New()

		m.Add(declare.Resource{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES})

		got := m.Snapshot()
		want := []declare.Resource{{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("Snapshot() = %+v, want %+v", got, want)
		}
	})

	t.Run("snapshot is an independent copy", func(t *testing.T) {
		t.Parallel()

		m := New()
		m.Add(declare.Resource{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES})

		snap := m.Snapshot()
		snap[0].Name = "mutated"

		got := m.Snapshot()
		if got[0].Name != "main" {
			t.Fatalf("mutating a snapshot affected the registry: got %q", got[0].Name)
		}
	})

	t.Run("reset clears entries", func(t *testing.T) {
		t.Parallel()

		m := New()
		m.Add(declare.Resource{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES})

		m.Reset()

		if got := m.Snapshot(); len(got) != 0 {
			t.Fatalf("Snapshot() after Reset = %+v, want empty", got)
		}

		m.Add(declare.Resource{Name: "second", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES})
		got := m.Snapshot()
		want := []declare.Resource{{Name: "second", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES}}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("Snapshot() after Reset+Add = %+v, want %+v", got, want)
		}
	})

	t.Run("concurrent add", func(t *testing.T) {
		t.Parallel()

		m := New()
		var wg sync.WaitGroup
		for range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m.Add(declare.Resource{Name: "r", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES})
			}()
		}
		wg.Wait()

		if got := len(m.Snapshot()); got != 50 {
			t.Fatalf("Snapshot() len = %d, want 50", got)
		}
	})
}
