package manifest

import (
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestManifest(t *testing.T) {
	t.Parallel()

	t.Run("add and snapshot", func(t *testing.T) {
		t.Parallel()

		m := New()

		m.Add(Entry{Name: "main", Type: naming.TokenPostgres})

		got := m.Snapshot()
		want := []Entry{{Name: "main", Type: naming.TokenPostgres}}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("Snapshot() = %+v, want %+v", got, want)
		}
	})

	t.Run("snapshot is an independent copy", func(t *testing.T) {
		t.Parallel()

		m := New()
		m.Add(Entry{Name: "main", Type: naming.TokenPostgres})

		snap := m.Snapshot()
		snap[0].Name = "mutated"

		got := m.Snapshot()
		if got[0].Name != "main" {
			t.Fatalf("mutating a snapshot affected the manifest: got %q", got[0].Name)
		}
	})

	t.Run("reset clears entries", func(t *testing.T) {
		t.Parallel()

		m := New()
		m.Add(Entry{Name: "main", Type: naming.TokenPostgres})

		m.Reset()

		if got := m.Snapshot(); len(got) != 0 {
			t.Fatalf("Snapshot() after Reset = %+v, want empty", got)
		}

		m.Add(Entry{Name: "second", Type: naming.TokenPostgres})
		got := m.Snapshot()
		want := []Entry{{Name: "second", Type: naming.TokenPostgres}}
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
				m.Add(Entry{Name: "r", Type: naming.TokenPostgres})
			}()
		}
		wg.Wait()

		if got := len(m.Snapshot()); got != 50 {
			t.Fatalf("Snapshot() len = %d, want 50", got)
		}
	})
}
