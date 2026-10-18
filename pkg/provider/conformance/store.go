package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RunStore(t *testing.T, store keyvalue.Store) {
	t.Helper()

	ctx := context.Background()

	t.Run("an unwritten key is no entry", func(t *testing.T) {
		if _, err := store.Read(ctx, under(t, "never-written")); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatalf("Read() of a key never written = %v, want ErrNotFound", err)
		}
	})

	t.Run("a first write claims the key", func(t *testing.T) {
		key := under(t, "claimed")
		revision, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("one")})
		if err != nil {
			t.Fatalf("Write() of a new entry = %v, want it stored", err)
		}
		if revision == "" {
			t.Fatal("Write() returned an empty revision, and a compare-and-set has nothing to compare")
		}
		recorded, err := store.Read(ctx, key)
		if err != nil || !bytes.Equal(recorded.Value, text("one")) {
			t.Fatalf("Read() = %s, %v, want the value just written", recorded.Value, err)
		}
		if recorded.Revision != revision {
			t.Fatalf("Read() revision = %q, want the %q Write() reported", recorded.Revision, revision)
		}
	})

	t.Run("a value that is not JSON is refused and nothing lands", func(t *testing.T) {
		key, beside := under(t, "unreadable"), under(t, "beside")
		var refused refusal.Refusal
		if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: json.RawMessage("one")}); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Fatalf("Write() of a value that is not JSON = %v, want an %s refusal", err, refusal.CodeInvalid)
		}
		if err := store.WritePair(ctx,
			keyvalue.Entry{Key: beside, Value: text("one")},
			keyvalue.Entry{Key: key, Value: json.RawMessage("{")},
		); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Fatalf("WritePair() where one value is not JSON = %v, want an %s refusal", err, refusal.CodeInvalid)
		}
		for _, key := range []keyvalue.Key{key, beside} {
			if _, err := store.Read(ctx, key); !errors.Is(err, keyvalue.ErrNotFound) {
				t.Errorf("Read(%s) after a refused write = %v, want ErrNotFound", key, err)
			}
		}
	})

	t.Run("a second write at the same key must name a revision", func(t *testing.T) {
		key := under(t, "occupied")
		if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("one")}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("two")}); !errors.Is(err, keyvalue.ErrStale) {
			t.Fatalf("Write() at a taken key with no revision = %v, want ErrStale", err)
		}
	})

	t.Run("a write at the revision read wins and a later one loses", func(t *testing.T) {
		key := under(t, "compared")
		if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("one")}); err != nil {
			t.Fatal(err)
		}
		recorded, err := store.Read(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		recorded.Value = text("two")
		if _, err := store.Write(ctx, recorded); err != nil {
			t.Fatalf("Write() at the revision read = %v, want it stored", err)
		}
		recorded.Value = text("three")
		if _, err := store.Write(ctx, recorded); !errors.Is(err, keyvalue.ErrStale) {
			t.Fatalf("a second write at a revision that moved = %v, want ErrStale", err)
		}
	})

	t.Run("a pair lands whole or not at all", func(t *testing.T) {
		record, value := under(t, "pair", "record"), under(t, "pair", "value")
		if err := store.WritePair(ctx,
			keyvalue.Entry{Key: record, Value: text("one")},
			keyvalue.Entry{Key: value, Value: text("one")},
		); err != nil {
			t.Fatalf("WritePair() of two new entries = %v, want both stored", err)
		}
		recorded, err := store.Read(ctx, record)
		if err != nil {
			t.Fatal(err)
		}
		beside, err := store.Read(ctx, value)
		if err != nil {
			t.Fatal(err)
		}

		moved := beside
		moved.Revision = "a revision nobody wrote"
		recorded.Value, moved.Value = text("two"), text("two")
		if err := store.WritePair(ctx, recorded, moved); !errors.Is(err, keyvalue.ErrStale) {
			t.Fatalf("WritePair() where one half moved = %v, want ErrStale", err)
		}
		for _, key := range []keyvalue.Key{record, value} {
			recorded, err := store.Read(ctx, key)
			if err != nil || !bytes.Equal(recorded.Value, text("one")) {
				t.Fatalf("Read(%s) after a refused pair write = %s, %v, want the value from the write that landed", key, recorded.Value, err)
			}
		}
	})

	t.Run("a removal names the revision it read", func(t *testing.T) {
		key := under(t, "removed")
		revision, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("one")})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remove(ctx, key, "a revision nobody wrote"); !errors.Is(err, keyvalue.ErrStale) {
			t.Fatalf("Remove() at a revision that was never current = %v, want ErrStale", err)
		}
		if err := store.Remove(ctx, key, revision); err != nil {
			t.Fatalf("Remove() at the current revision = %v, want it gone", err)
		}
		if _, err := store.Read(ctx, key); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatalf("Read() after Remove() = %v, want ErrNotFound", err)
		}
	})

	t.Run("one tier's entries are not the other's", func(t *testing.T) {
		production, preview := conformanceIn(environment.TierProduction, t).Key("isolated"), conformanceIn(environment.TierPreview, t).Key("isolated")
		if _, err := store.Write(ctx, keyvalue.Entry{Key: production, Value: text("production")}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read(ctx, preview); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatalf("Read() of the preview key after only production was written = %v, want ErrNotFound", err)
		}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: preview, Value: text("preview")}); err != nil {
			t.Fatal(err)
		}
		recorded, err := store.Read(ctx, production)
		if err != nil || !bytes.Equal(recorded.Value, text("production")) {
			t.Fatalf("Read() of the production key = %s, %v, want the production value untouched", recorded.Value, err)
		}
	})

	t.Run("List answers with everything under a prefix", func(t *testing.T) {
		leaves := []keyvalue.Key{
			under(t, "tree"),
			under(t, "tree", "a"),
			under(t, "tree", "b", "one"),
			under(t, "tree", "b", "two"),
		}
		for _, key := range leaves {
			if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text(key.String())}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: under(t, "treeish"), Value: text("not under it")}); err != nil {
			t.Fatal(err)
		}

		listed, err := store.List(ctx, conformanceIn(environment.TierProduction, t), "tree")
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if len(listed) != len(leaves) {
			t.Fatalf("List() returned %d entries, want the %d written at and under the prefix and nothing beside them", len(listed), len(leaves))
		}
		for _, entry := range listed {
			if !bytes.Equal(entry.Value, text(entry.Key.String())) {
				t.Errorf("List() returned %s containing %s, want the value written at that key", entry.Key, entry.Value)
			}
			if entry.Revision == "" {
				t.Errorf("List() returned %s with no revision, and a caller cannot then remove it", entry.Key)
			}
		}

		deeper, err := store.List(ctx, conformanceIn(environment.TierProduction, t), "tree", "b")
		if err != nil || len(deeper) != 2 {
			t.Fatalf("List() of a deeper prefix returned %d entries, %v, want 2", len(deeper), err)
		}

		whole, err := store.List(ctx, conformanceIn(environment.TierProduction, t))
		if err != nil || len(whole) != len(leaves)+1 {
			t.Fatalf("List() of the partition this suite writes in returned %d entries, %v, want the %d written in it", len(whole), err, len(leaves)+1)
		}
	})

	t.Run("List answers from its own partition and none that extends it", func(t *testing.T) {
		in := conformanceIn(environment.TierProduction, t)
		deeper := keyvalue.Partition{Tier: in.Tier, Root: in.Root, Path: append(slices.Clone(in.Path), "tree")}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: in.Key("tree", "own"), Value: text("own")}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: deeper.Key("own"), Value: text("deeper")}); err != nil {
			t.Fatal(err)
		}

		for _, c := range []struct {
			in   keyvalue.Partition
			want string
		}{{in, "own"}, {deeper, "deeper"}} {
			listed, err := store.List(ctx, c.in)
			if err != nil || len(listed) != 1 || !bytes.Equal(listed[0].Value, text(c.want)) {
				t.Fatalf("List(%s) = %v, %v, want the one entry written in that partition", c.in, listed, err)
			}
		}
	})

	t.Run("a segment is kept whole whatever it contains", func(t *testing.T) {
		segments := []string{"ocel/web@sha256:0123", "a#b", "100%", "a|b", "a+b", ".hidden", "..", "tab\there", "é"}
		for _, segment := range segments {
			if _, err := store.Write(ctx, keyvalue.Entry{Key: under(t, "builds", segment), Value: text(segment)}); err != nil {
				t.Fatalf("Write() at a segment %q = %v", segment, err)
			}
		}

		listed, err := store.List(ctx, conformanceIn(environment.TierProduction, t), "builds")
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		var got []string
		for _, entry := range listed {
			rest, named := entry.Key.Under("builds")
			if !named || len(rest) != 1 {
				t.Fatalf("List() returned %s, want one segment beneath builds", entry.Key)
			}
			if !bytes.Equal(entry.Value, text(rest[0])) {
				t.Errorf("List() returned %s containing %s, want the value written at that segment", entry.Key, entry.Value)
			}
			got = append(got, rest[0])
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(segments))
		if !slices.Equal(got, want) {
			t.Fatalf("List() named segments %q, want %q", got, want)
		}
	})
}
