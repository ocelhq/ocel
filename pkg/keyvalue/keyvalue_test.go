package keyvalue_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var values = keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootValues, Path: []string{"shop"}}

type moving struct {
	keyvalue.Store
	recorded keyvalue.Entry
	moves    int
	removed  bool
}

func (m *moving) Read(context.Context, keyvalue.Key) (keyvalue.Entry, error) {
	if m.removed {
		return keyvalue.Entry{}, keyvalue.ErrNotFound
	}
	return m.recorded, nil
}

func (m *moving) Remove(context.Context, keyvalue.Key, keyvalue.Revision) error {
	if m.moves > 0 {
		m.moves--
		m.recorded.Revision += "'"
		return keyvalue.ErrStale
	}
	m.removed = true
	return nil
}

func TestForgetReadsAgainWhenTheEntryMovedUnderIt(t *testing.T) {
	moved := &moving{recorded: keyvalue.Entry{Key: values.Key("cells"), Revision: "one"}, moves: 2}
	if err := keyvalue.Forget(context.Background(), moved, moved.recorded.Key); err != nil {
		t.Fatalf("Forget() of an entry rewritten twice = %v, want it removed at the revision it ended at", err)
	}
	if !moved.removed {
		t.Fatal("Forget() reported the entry gone while it still existed")
	}
}

func TestForgetRefusesToReportAnEntryGoneThatKeepsMoving(t *testing.T) {
	moved := &moving{recorded: keyvalue.Entry{Key: values.Key("cells"), Revision: "one"}, moves: 100}
	if err := keyvalue.Forget(context.Background(), moved, moved.recorded.Key); err == nil {
		t.Fatal("Forget() of an entry it never removed = nil, and every caller reads that as removed")
	}
}

func TestAValueThatIsNotJSONIsRefused(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"2", `{"a":1}`, `"text"`, "[]"} {
		if err := keyvalue.RefuseNonJSON(keyvalue.Entry{Key: values.Key("cells"), Value: json.RawMessage(value)}); err != nil {
			t.Errorf("RefuseNonJSON(%q) = %v, want it accepted", value, err)
		}
	}
	for _, value := range []string{"", "one", "{", "sk_live_secret"} {
		var refused refusal.Refusal
		err := keyvalue.RefuseNonJSON(keyvalue.Entry{Key: values.Key("cells"), Value: json.RawMessage(value)})
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("RefuseNonJSON(%q) = %v, want an %s refusal", value, err, refusal.CodeInvalid)
		}
	}
}

func TestAKeyMustNameATierARootAndEverySegment(t *testing.T) {
	t.Parallel()

	for _, key := range []keyvalue.Key{
		{Partition: keyvalue.Partition{Root: keyvalue.RootValues}, Path: []string{"cells"}},
		{Partition: keyvalue.Partition{Tier: "staging", Root: keyvalue.RootValues}, Path: []string{"cells"}},
		{Partition: keyvalue.Partition{Tier: environment.TierPreview}, Path: []string{"cells"}},
		{Partition: keyvalue.Partition{Tier: environment.TierPreview, Root: keyvalue.RootValues, Path: []string{""}}, Path: []string{"cells"}},
		values.Key(),
		values.Key("cells", ""),
	} {
		var refused refusal.Refusal
		if err := keyvalue.RefuseMalformedKey(key); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("RefuseMalformedKey(%s) = %v, want an %s refusal", key, err, refusal.CodeInvalid)
		}
	}
	if err := keyvalue.RefuseMalformedKey(values.Key("cells", "a/b")); err != nil {
		t.Errorf("RefuseMalformedKey() of a key naming every segment = %v, want it accepted", err)
	}
}

func TestAKeyIsUnderEveryPrefixOfItsPathAndNoOther(t *testing.T) {
	t.Parallel()

	key := values.Key("bindings", "db/primary", "records")
	if rest, under := key.Under("bindings"); !under || len(rest) != 2 || rest[0] != "db/primary" {
		t.Errorf("Under(bindings) = %q, %v, want the two segments beneath it", rest, under)
	}
	if _, under := key.Under("bindings", "db"); under {
		t.Error("Under(bindings, db) = true, and a segment is matched whole, never by its prefix")
	}
	if _, under := key.Under("bindings", "db/primary", "records"); under {
		t.Error("Under() of the key's whole path = true, want only what lies beneath it")
	}
}

func TestPartitionKeyDoesNotShareItsPathWithTheKeysItNames(t *testing.T) {
	t.Parallel()

	path := []string{"cells", "one"}
	key := values.Key(path...)
	path[1] = "two"
	if key.Path[1] != "one" {
		t.Fatalf("Key() = %s after its caller reused the path, want the path it was given", key)
	}
}

func TestTwoKeysThatDifferAreNeverWrittenTheSame(t *testing.T) {
	t.Parallel()

	for _, pair := range [][2]keyvalue.Key{
		{values.Key("cells", "/a", "b/c"), values.Key("cells", "/a/b", "c")},
		{values.Key("a|b"), keyvalue.Partition{Tier: values.Tier, Root: values.Root, Path: []string{"shop|a"}}.Key("b")},
		{values.Key("a%2Fb"), values.Key("a/b")},
	} {
		if pair[0].String() == pair[1].String() {
			t.Errorf("%v and %v are both written %q", pair[0].Path, pair[1].Path, pair[0].String())
		}
	}
}
