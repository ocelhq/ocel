package vars

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func source() Coordinate {
	return Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"}
}

func consumer() Coordinate {
	return Coordinate{Slug: "shop", Folder: "/checkout", Key: "STRIPE_API_KEY"}
}

func setReferenced(t *testing.T, store *Store, plaintext string) {
	t.Helper()
	if _, err := store.Set(context.Background(), source(), plaintext, nil); err != nil {
		t.Fatalf("Set the source value err = %v", err)
	}
	if _, err := store.SetReference(context.Background(), consumer(), source(), nil); err != nil {
		t.Fatalf("SetReference err = %v", err)
	}
}

func TestReference(t *testing.T) {
	t.Run("reads as the value it points at", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		got, err := store.Get(context.Background(), consumer(), true)
		if err != nil {
			t.Fatalf("Get err = %v", err)
		}
		if got.Plaintext != "sk_live_shared" {
			t.Errorf("resolved plaintext = %q, want the source's value", got.Plaintext)
		}
		if got.Target != source() {
			t.Errorf("Target = %v, want %v", got.Target, source())
		}

		for sk, item := range ddb.items[PartitionKey(consumer().Slug, store.Class)] {
			if len(binaryAttr(item, "ciphertext")) != 0 {
				t.Errorf("%s carries ciphertext of its own; a reference holds an address, not a copy", sk)
			}
		}
	})

	t.Run("resolves to the sources current value", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "first")

		if _, err := store.Set(context.Background(), source(), "rotated", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}

		got, err := store.Get(context.Background(), consumer(), true)
		if err != nil {
			t.Fatalf("Get err = %v", err)
		}
		if got.Plaintext != "rotated" {
			t.Errorf("plaintext after an edit at the source = %q, want %q with no write to the reference at all", got.Plaintext, "rotated")
		}
	})

	t.Run("reveal resolves one across projects", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		values, err := store.Reveal(context.Background(), consumer().Slug, []Coordinate{{Folder: consumer().Folder, Key: consumer().Key}})
		if err != nil {
			t.Fatalf("Reveal err = %v", err)
		}
		if len(values) != 1 {
			t.Fatalf("Reveal returned %d values, want 1", len(values))
		}
		if values[0].Plaintext != "sk_live_shared" {
			t.Errorf("revealed plaintext = %q, want the source's value", values[0].Plaintext)
		}
		if values[0].Coordinate != consumer() {
			t.Errorf("revealed coordinate = %v, want the cell that was asked for", values[0].Coordinate)
		}
	})

	t.Run("a chain the index was too late to refuse fails the read", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		elsewhere := Coordinate{Slug: "platform", Key: "SHARED_SECRET"}
		if _, err := store.Set(context.Background(), elsewhere, "another value", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}
		ddb.indexBehind = true
		if _, err := store.SetReference(context.Background(), source(), elsewhere, nil); err != nil {
			t.Fatalf("SetReference against a lagging index err = %v, want it accepted: that is the case this read has to catch", err)
		}
		ddb.indexBehind = false

		_, err := store.Get(context.Background(), consumer(), true)
		if !errors.Is(err, ErrWouldDeepen) {
			t.Fatalf("Get through a two-hop chain err = %v, want ErrWouldDeepen", err)
		}
		if !strings.Contains(err.Error(), source().String()) {
			t.Errorf("failure = %q, want it to name the hop that turned out to be a reference", err)
		}
		if _, err := store.Reveal(context.Background(), consumer().Slug, []Coordinate{{Folder: consumer().Folder, Key: consumer().Key}}); !errors.Is(err, ErrWouldDeepen) {
			t.Errorf("Reveal through a two-hop chain err = %v, want the batch to fail the same way", err)
		}
	})

	t.Run("to a deleted source fails the read rather than reading as unset", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		if _, err := store.Delete(context.Background(), source(), nil); err != nil {
			t.Fatalf("Delete err = %v", err)
		}

		_, err := store.Get(context.Background(), consumer(), true)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get through a reference to a deleted value err = %v, want ErrNotFound", err)
		}
		if !strings.Contains(err.Error(), source().String()) {
			t.Errorf("failure = %q, want it to name the cell that no longer holds a value", err)
		}
		if _, err := store.Reveal(context.Background(), consumer().Slug, []Coordinate{{Folder: consumer().Folder, Key: consumer().Key}}); err == nil {
			t.Error("Reveal err = nil, want the batch to fail rather than return a set of variables with one silently missing")
		}
	})

	t.Run("list shows where it points", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		found, err := store.List(context.Background(), consumer().Slug)
		if err != nil {
			t.Fatalf("List err = %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("List = %v, want the one reference", found)
		}
		if found[0].Target != source() {
			t.Errorf("Target = %v, want %v", found[0].Target, source())
		}
		if found[0].Size != 0 {
			t.Errorf("Size = %d, want 0: a reference holds an address, and the value's size is the source's", found[0].Size)
		}
	})
}

func TestSetReference(t *testing.T) {
	t.Run("rejects a target that is itself a reference", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		third := Coordinate{Slug: "admin", Key: "STRIPE_API_KEY"}
		_, err := store.SetReference(context.Background(), third, consumer(), nil)
		if !errors.Is(err, ErrWouldDeepen) {
			t.Fatalf("SetReference at a reference err = %v, want ErrWouldDeepen", err)
		}
		if _, err := store.Get(context.Background(), third, false); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after the rejected write err = %v, want the write to have landed nowhere", err)
		}
	})

	t.Run("rejects a cell others already reference", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		elsewhere := Coordinate{Slug: "platform", Key: "SHARED_SECRET"}
		if _, err := store.Set(context.Background(), elsewhere, "another value", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}

		_, err := store.SetReference(context.Background(), source(), elsewhere, nil)
		if !errors.Is(err, ErrWouldDeepen) {
			t.Fatalf("SetReference on a referenced cell err = %v, want ErrWouldDeepen", err)
		}
		if !strings.Contains(err.Error(), consumer().String()) {
			t.Errorf("refusal = %q, want it to name %s, which is what would have been left reading a reference", err, consumer())
		}
	})

	t.Run("rejects a cell pointed at itself", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		if _, err := store.Set(context.Background(), source(), "a value", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}

		if _, err := store.SetReference(context.Background(), source(), source(), nil); !errors.Is(err, ErrWouldDeepen) {
			t.Fatalf("self-referencing SetReference err = %v, want ErrWouldDeepen", err)
		}
	})

	t.Run("rejects an environment specific target", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		target := source()
		target.Environment = "staging"
		if _, err := store.Set(context.Background(), target, "staging's own value", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}

		_, err := store.SetReference(context.Background(), consumer(), target, nil)
		if err == nil {
			t.Fatal("SetReference at an override err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "class-wide") {
			t.Errorf("refusal = %q, want it to say a reference resolves against the class-wide value", err)
		}
	})

	t.Run("may be written before the value it reads is set", func(t *testing.T) {
		store, _, _ := newTestStore(t)

		if _, err := store.SetReference(context.Background(), consumer(), source(), nil); err != nil {
			t.Fatalf("SetReference at a cell not set yet err = %v, want it accepted", err)
		}
		_, err := store.Get(context.Background(), consumer(), true)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get before the value was set err = %v, want ErrNotFound", err)
		}
		if !strings.Contains(err.Error(), source().String()) {
			t.Errorf("failure = %q, want it to name the cell that holds nothing", err)
		}

		if _, err := store.Set(context.Background(), source(), "sk_live_shared", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}
		got, err := store.Get(context.Background(), consumer(), true)
		if err != nil {
			t.Fatalf("Get err = %v", err)
		}
		if got.Plaintext != "sk_live_shared" {
			t.Errorf("plaintext = %q, want the value that landed at the source with no second write to the reference", got.Plaintext)
		}
	})

	t.Run("set refuses to edit through one", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		_, err := store.Set(context.Background(), consumer(), "an edit made in the wrong place", nil)
		if !errors.Is(err, ErrIsReference) {
			t.Fatalf("Set through a reference err = %v, want ErrIsReference", err)
		}
		if !strings.Contains(err.Error(), source().String()) {
			t.Errorf("refusal = %q, want it to name where the value is edited", err)
		}

		got, err := store.Get(context.Background(), consumer(), true)
		if err != nil {
			t.Fatalf("Get err = %v", err)
		}
		if got.Plaintext != "sk_live_shared" {
			t.Errorf("value after the refused edit = %q, want it untouched", got.Plaintext)
		}
	})

	t.Run("takes the next version of the cell it replaces", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		if _, err := store.Set(context.Background(), source(), "the shared value", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}
		if _, err := store.Set(context.Background(), consumer(), "a copy someone pasted", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}

		written, err := store.SetReference(context.Background(), consumer(), source(), nil)
		if err != nil {
			t.Fatalf("SetReference err = %v", err)
		}
		if written.Version != 2 {
			t.Errorf("version = %d, want 2", written.Version)
		}

		versions, err := store.Versions(context.Background(), consumer())
		if err != nil {
			t.Fatalf("Versions err = %v", err)
		}
		if len(versions) != 2 {
			t.Fatalf("Versions = %v, want both the value and the reference that replaced it", versions)
		}
	})
}

func TestReferences(t *testing.T) {
	t.Run("answers what points at a value from the index", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		second := Coordinate{Slug: "admin", Key: "PAYMENTS_KEY"}
		if _, err := store.SetReference(context.Background(), second, source(), nil); err != nil {
			t.Fatalf("SetReference err = %v", err)
		}
		if _, err := store.Set(context.Background(), Coordinate{Slug: "admin", Key: "ADMIN_TOKEN"}, "unrelated", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}

		found, err := store.References(context.Background(), source())
		if err != nil {
			t.Fatalf("References err = %v", err)
		}
		want := []Coordinate{second, consumer()}
		if len(found) != len(want) {
			t.Fatalf("References = %v, want %v", found, want)
		}
		for i, c := range want {
			if found[i] != c {
				t.Errorf("References[%d] = %v, want %v", i, found[i], c)
			}
		}

		indexed := false
		for _, query := range ddb.queries {
			if aws.ToString(query.IndexName) == IndexName {
				indexed = true
			}
		}
		if !indexed {
			t.Error("References never queried the reverse index; the question it answers is a scan without one")
		}
	})

	t.Run("counts only the pointers that still point", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		elsewhere := Coordinate{Slug: "platform", Key: "OTHER_KEY"}
		if _, err := store.Set(context.Background(), elsewhere, "another value", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}
		if _, err := store.SetReference(context.Background(), consumer(), elsewhere, nil); err != nil {
			t.Fatalf("re-pointing SetReference err = %v", err)
		}

		found, err := store.References(context.Background(), source())
		if err != nil {
			t.Fatalf("References err = %v", err)
		}
		if len(found) != 0 {
			t.Errorf("References after the pointer moved = %v, want none: only the version history still names the old target", found)
		}
	})

	t.Run("removing a reference needs no unlink step", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		deleted, err := store.Delete(context.Background(), consumer(), nil)
		if err != nil {
			t.Fatalf("Delete err = %v", err)
		}
		if !deleted {
			t.Error("Delete reported nothing to remove, want the reference removed")
		}

		if _, err := store.Get(context.Background(), consumer(), true); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get after removing the reference err = %v, want ErrNotFound", err)
		}
		found, err := store.References(context.Background(), source())
		if err != nil {
			t.Fatalf("References err = %v", err)
		}
		if len(found) != 0 {
			t.Errorf("References after the removal = %v, want none", found)
		}

		value, err := store.Get(context.Background(), source(), true)
		if err != nil {
			t.Fatalf("Get the source err = %v", err)
		}
		if value.Plaintext != "sk_live_shared" {
			t.Errorf("source value = %q, want removing a consumer to leave it alone", value.Plaintext)
		}
	})
}

func TestReferenceOwners(t *testing.T) {
	t.Run("names the owner of each cell that reads elsewhere", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		setReferenced(t, store, "sk_live_shared")

		second := Coordinate{Slug: consumer().Slug, Key: "PAYMENTS_KEY"}
		if _, err := store.SetReference(context.Background(), second, source(), nil); err != nil {
			t.Fatalf("SetReference err = %v", err)
		}
		own := Coordinate{Slug: consumer().Slug, Key: "LOCAL_KEY"}
		if _, err := store.Set(context.Background(), own, "a value of its own", nil); err != nil {
			t.Fatalf("Set err = %v", err)
		}
		within := Coordinate{Slug: consumer().Slug, Folder: "/admin", Key: "LOCAL_KEY"}
		if _, err := store.SetReference(context.Background(), within, own, nil); err != nil {
			t.Fatalf("SetReference err = %v", err)
		}

		owners, err := store.ReferenceOwners(context.Background(), consumer().Slug)
		if err != nil {
			t.Fatalf("ReferenceOwners err = %v", err)
		}
		want := map[Coordinate]string{consumer(): source().Slug, second: source().Slug}
		if len(owners) != len(want) {
			t.Fatalf("ReferenceOwners = %v, want %v", owners, want)
		}
		for cell, owner := range want {
			if owners[cell] != owner {
				t.Errorf("ReferenceOwners[%v] = %q, want %q", cell, owners[cell], owner)
			}
		}
	})
}
