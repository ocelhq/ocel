package vars

import (
	"context"
	"strings"
	"testing"
)

func TestPurge(t *testing.T) {
	t.Run("removes every row a project holds", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		cells := []Coordinate{
			{Slug: "shop", Key: "STRIPE_API_KEY"},
			{Slug: "shop", Key: "POSTHOG_ID", Folder: "/web"},
			{Slug: "shop", Key: "STRIPE_API_KEY", Environment: "pr-1"},
		}
		for _, c := range cells {
			for range 3 {
				if _, err := store.Set(context.Background(), c, "value-of-"+c.Key, nil); err != nil {
					t.Fatalf("Set %+v err = %v", c, err)
				}
			}
		}
		if _, err := store.Delete(context.Background(), cells[0], nil); err != nil {
			t.Fatalf("Delete err = %v", err)
		}

		removed, err := store.Purge(context.Background(), "shop")
		if err != nil {
			t.Fatalf("Purge err = %v", err)
		}
		if removed == 0 {
			t.Error("Purge reported nothing removed")
		}
		if left := ddb.items[PartitionKey("shop", store.Class)]; len(left) != 0 {
			for sk := range left {
				t.Errorf("Purge left %s behind", sk)
			}
		}
	})

	t.Run("leaves other projects and classes alone", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		preview := *store
		preview.Class = "preview"

		for _, s := range []*Store{store, &preview} {
			if _, err := s.Set(context.Background(), Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}, "v", nil); err != nil {
				t.Fatalf("Set err = %v", err)
			}
			if _, err := s.Set(context.Background(), Coordinate{Slug: "other", Key: "STRIPE_API_KEY"}, "v", nil); err != nil {
				t.Fatalf("Set err = %v", err)
			}
		}

		if _, err := store.Purge(context.Background(), "shop"); err != nil {
			t.Fatalf("Purge err = %v", err)
		}

		for _, pk := range []string{
			PartitionKey("other", store.Class),
			PartitionKey("shop", preview.Class),
			PartitionKey("other", preview.Class),
		} {
			if len(ddb.items[pk]) == 0 {
				t.Errorf("Purge emptied %s, which belongs to another project or class", pk)
			}
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		if _, err := store.Set(context.Background(), testCoordinate(), "v", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}
		if _, err := store.Purge(context.Background(), "shop"); err != nil {
			t.Fatalf("Purge err = %v", err)
		}

		removed, err := store.Purge(context.Background(), "shop")
		if err != nil {
			t.Fatalf("second Purge err = %v", err)
		}
		if removed != 0 {
			t.Errorf("second Purge removed %d rows, want 0", removed)
		}
	})

	t.Run("batches within the transaction limit", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		for i := range purgeBatch {
			c := Coordinate{Slug: "shop", Key: "KEY_" + string(rune('A'+i%26)) + string(rune('A'+i/26))}
			if _, err := store.Set(context.Background(), c, "v", nil); err != nil {
				t.Fatalf("Set %+v err = %v", c, err)
			}
		}
		before := len(ddb.transactions)

		removed, err := store.Purge(context.Background(), "shop")
		if err != nil {
			t.Fatalf("Purge err = %v", err)
		}
		if removed != 2*purgeBatch {
			t.Errorf("Purge removed %d rows, want %d (a current value and a history row each)", removed, 2*purgeBatch)
		}
		for _, tx := range ddb.transactions[before:] {
			if len(tx) > purgeBatch {
				t.Errorf("a purge transaction carried %d writes, over the %d-item limit", len(tx), purgeBatch)
			}
		}
		if len(ddb.items[PartitionKey("shop", store.Class)]) != 0 {
			t.Error("Purge left rows behind")
		}
	})

	t.Run("requires a project", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		_, err := store.Purge(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "slug") {
			t.Errorf("Purge err = %v, want it to require a project slug", err)
		}
	})
}
