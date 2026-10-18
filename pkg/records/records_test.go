package records_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var values = records.Partition{Tier: environment.TierProduction, Root: records.Root("values"), Path: []string{"shop"}}

type movingStore struct {
	records.Store
	recorded records.Entry
	moves    int
	removed  bool
}

func (m *movingStore) Read(context.Context, records.Key) (records.Entry, error) {
	if m.removed {
		return records.Entry{}, records.ErrNotFound
	}
	return m.recorded, nil
}

func (m *movingStore) Remove(context.Context, records.Key, records.Revision) error {
	if m.moves > 0 {
		m.moves--
		m.recorded.Revision += "'"
		return records.ErrStale
	}
	m.removed = true
	return nil
}

func TestForgetReadsAgainWhenTheEntryMovedUnderIt(t *testing.T) {
	moved := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one"}, moves: 2}
	if err := records.Forget(context.Background(), moved, moved.recorded.Key); err != nil {
		t.Fatalf("Forget() of an entry rewritten twice = %v, want it removed at the revision it ended at", err)
	}
	if !moved.removed {
		t.Fatal("Forget() reported the entry gone while it still existed")
	}
}

func TestForgetRefusesToReportAnEntryGoneThatKeepsMoving(t *testing.T) {
	moved := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one"}, moves: 100}
	if err := records.Forget(context.Background(), moved, moved.recorded.Key); err == nil {
		t.Fatal("Forget() of an entry it never removed = nil, and every caller reads that as removed")
	}
}

func TestForgetOutlastsSevenWritersRacingItToTheEntry(t *testing.T) {
	moved := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one"}, moves: 7}
	if err := records.Forget(context.Background(), moved, moved.recorded.Key); err != nil || !moved.removed {
		t.Errorf("Forget() of an entry rewritten seven times = %v, removed = %v, want it removed: Forget and Change race the same writers", err, moved.removed)
	}
}

type rewritingStore struct {
	movingStore
	rewrite json.RawMessage
}

func (r *rewritingStore) Remove(ctx context.Context, key records.Key, revision records.Revision) error {
	if r.rewrite != nil {
		r.recorded.Value, r.recorded.Revision, r.rewrite = r.rewrite, r.recorded.Revision+"'", nil
		return records.ErrStale
	}
	return r.movingStore.Remove(ctx, key, revision)
}

func matchMine(seen *[]string) func(records.Entry) (bool, error) {
	return func(recorded records.Entry) (bool, error) {
		*seen = append(*seen, string(recorded.Value))
		return string(recorded.Value) == `"mine"`, nil
	}
}

func TestForgetMatchingKeepsAnEntryThatDoesNotMatch(t *testing.T) {
	kept := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one", Value: []byte(`"theirs"`)}}
	var seen []string
	if err := records.ForgetMatching(context.Background(), kept, kept.recorded.Key, matchMine(&seen)); err != nil {
		t.Fatalf("ForgetMatching() = %v", err)
	}
	if kept.removed {
		t.Error("ForgetMatching() removed an entry its match refused")
	}
}

func TestForgetMatchingRemovesAMatchingEntryAtTheRevisionItEndedAt(t *testing.T) {
	moved := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one", Value: []byte(`"mine"`)}, moves: 2}
	var seen []string
	if err := records.ForgetMatching(context.Background(), moved, moved.recorded.Key, matchMine(&seen)); err != nil {
		t.Fatalf("ForgetMatching() of a matching entry rewritten twice = %v, want it removed", err)
	}
	if !moved.removed || len(seen) != 3 {
		t.Errorf("ForgetMatching() removed = %v after matching %d reads, want it removed after matching each of the 3", moved.removed, len(seen))
	}
}

func TestForgetMatchingKeepsAnEntryRewrittenUnderItIntoOneThatNoLongerMatches(t *testing.T) {
	moved := &rewritingStore{
		movingStore: movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one", Value: []byte(`"mine"`)}},
		rewrite:     []byte(`"theirs"`),
	}
	var seen []string
	if err := records.ForgetMatching(context.Background(), moved, moved.recorded.Key, matchMine(&seen)); err != nil {
		t.Fatalf("ForgetMatching() = %v", err)
	}
	if moved.removed {
		t.Error("ForgetMatching() removed an entry rewritten under it into one its match refuses")
	}
	if want := []string{`"mine"`, `"theirs"`}; !slices.Equal(seen, want) {
		t.Errorf("the match saw %v, want %v: each attempt matches what is recorded now", seen, want)
	}
}

func TestForgetMatchingPassesOnTheMatchsError(t *testing.T) {
	recorded := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one", Value: []byte(`"mine"`)}}
	unreadable := errors.New("unreadable")
	err := records.ForgetMatching(context.Background(), recorded, recorded.recorded.Key, func(records.Entry) (bool, error) { return true, unreadable })
	if !errors.Is(err, unreadable) || recorded.removed {
		t.Errorf("ForgetMatching() = %v with removed = %v, want the match's error and the entry kept", err, recorded.removed)
	}
}

func TestAValueThatIsNotJSONIsRefused(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"2", `{"a":1}`, `"text"`, "[]"} {
		if err := records.RefuseUnwritable(records.Entry{Key: values.Key("cells"), Value: json.RawMessage(value)}); err != nil {
			t.Errorf("RefuseUnwritable(%q) = %v, want it accepted", value, err)
		}
	}
	for _, value := range []string{"", "one", "{", "sk_live_secret"} {
		var refused refusal.Refusal
		err := records.RefuseUnwritable(records.Entry{Key: values.Key("cells"), Value: json.RawMessage(value)})
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("RefuseUnwritable(%q) = %v, want an %s refusal", value, err, refusal.CodeInvalid)
		}
	}
}

func TestAKeyMustNameATierARootAndEverySegment(t *testing.T) {
	t.Parallel()

	for _, key := range []records.Key{
		{Partition: records.Partition{Root: records.Root("values")}, Path: []string{"cells"}},
		{Partition: records.Partition{Tier: "staging", Root: records.Root("values")}, Path: []string{"cells"}},
		{Partition: records.Partition{Tier: environment.TierPreview}, Path: []string{"cells"}},
		{Partition: records.Partition{Tier: environment.TierPreview, Root: records.Root("values"), Path: []string{""}}, Path: []string{"cells"}},
		values.Key(),
		values.Key("cells", ""),
	} {
		var refused refusal.Refusal
		if err := records.RefuseMalformedKey(key); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("RefuseMalformedKey(%s) = %v, want an %s refusal", key, err, refusal.CodeInvalid)
		}
	}
	if err := records.RefuseMalformedKey(values.Key("cells", "a/b")); err != nil {
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

	for _, pair := range [][2]records.Key{
		{values.Key("cells", "/a", "b/c"), values.Key("cells", "/a/b", "c")},
		{values.Key("a|b"), records.Partition{Tier: values.Tier, Root: values.Root, Path: []string{"shop|a"}}.Key("b")},
		{values.Key("a%2Fb"), values.Key("a/b")},
	} {
		if pair[0].String() == pair[1].String() {
			t.Errorf("%v and %v are both written %q", pair[0].Path, pair[1].Path, pair[0].String())
		}
	}
}

func TestASegmentIsJoinedWholeWhateverSeparatorOrReservedCharacterItCarries(t *testing.T) {
	t.Parallel()

	segments := []string{"ghcr.io/acme/web@sha256:ab", "50%#off", "a|b"}
	joined := records.JoinSegments(segments, "#", "/|")
	if want := "ghcr.io%2Facme%2Fweb@sha256:ab#50%25%23off#a%7Cb"; joined != want {
		t.Errorf("JoinSegments(%q) = %q, want %q", segments, joined, want)
	}
	if split, err := records.SplitSegments(joined, "#"); err != nil || !slices.Equal(split, segments) {
		t.Errorf("SplitSegments(%q) = %q, %v, want the segments it was joined from, %q", joined, split, err, segments)
	}
}

func TestASegmentHoldingAPercentThatEscapesNoByteIsRefused(t *testing.T) {
	t.Parallel()

	for _, joined := range []string{"50%", "50%2", "a#50%zzoff"} {
		var refused refusal.Refusal
		if split, err := records.SplitSegments(joined, "#"); !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
			t.Errorf("SplitSegments(%q) = %q, %v, want a %s refusal: JoinSegments escapes every %%, so ocel never wrote it", joined, split, err, refusal.CodeDenied)
		}
	}
}

func TestASegmentEscapedInLowerCaseHexIsRefused(t *testing.T) {
	t.Parallel()

	for _, joined := range []string{"a%2fb", "50%2a", "ghcr.io%2Facme%2fweb"} {
		var refused refusal.Refusal
		if split, err := records.SplitSegments(joined, "#"); !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
			t.Errorf("SplitSegments(%q) = %q, %v, want a %s refusal: JoinSegments escapes in upper-case hex, so one segment has one stored key", joined, split, err, refusal.CodeDenied)
		}
	}
}

func TestEveryRevisionMintedIsNew(t *testing.T) {
	t.Parallel()

	first, err := records.NewRevision()
	if err != nil {
		t.Fatal(err)
	}
	second, err := records.NewRevision()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 32 {
		t.Errorf("NewRevision() minted %q then %q, want two different 32-character tokens", first, second)
	}
}

func (m *movingStore) Write(_ context.Context, entry records.Entry) (records.Revision, error) {
	if entry.Revision != m.recorded.Revision {
		return "", records.ErrStale
	}
	if m.moves > 0 {
		m.moves--
		m.recorded.Revision += "'"
		m.recorded.Value = []byte(`"theirs"`)
		return "", records.ErrStale
	}
	m.recorded = entry
	return entry.Revision, nil
}

func TestChangeReadsAgainAndReappliesTheChangeWhenTheEntryMovedUnderIt(t *testing.T) {
	moved := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one", Value: []byte(`"mine"`)}, moves: 2}
	var seen []string
	err := records.Change(context.Background(), moved, moved.recorded.Key, func(recorded records.Entry) ([]byte, bool, error) {
		seen = append(seen, string(recorded.Value))
		return []byte(`"changed"`), true, nil
	})
	if err != nil {
		t.Fatalf("Change() of an entry rewritten twice = %v, want the change written at the revision it ended at", err)
	}
	if want := []string{`"mine"`, `"theirs"`, `"theirs"`}; !slices.Equal(seen, want) {
		t.Errorf("the change saw %v, want %v: each attempt starts from what is recorded", seen, want)
	}
	if string(moved.recorded.Value) != `"changed"` {
		t.Errorf("recorded %s, want the change", moved.recorded.Value)
	}
}

func TestChangeOutlastsSevenWritersRacingItToTheEntry(t *testing.T) {
	moved := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one", Value: []byte(`"mine"`)}, moves: 7}
	err := records.Change(context.Background(), moved, moved.recorded.Key, func(records.Entry) ([]byte, bool, error) {
		return []byte(`"changed"`), true, nil
	})
	if err != nil || string(moved.recorded.Value) != `"changed"` {
		t.Errorf("Change() of an entry rewritten seven times = %v, recorded %s, want the change written: parallel previews of one project race to its records", err, moved.recorded.Value)
	}
}

func TestChangeWritesNothingWhenTheChangeLeavesTheEntryAlone(t *testing.T) {
	moved := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one", Value: []byte(`"mine"`)}, moves: 100}
	err := records.Change(context.Background(), moved, moved.recorded.Key, func(records.Entry) ([]byte, bool, error) {
		return nil, false, nil
	})
	if err != nil || moved.moves != 100 {
		t.Errorf("Change() = %v after %d writes, want nil and no write", err, 100-moved.moves)
	}
}

func TestChangeRefusesToReportAWriteThatKeepsGoingStale(t *testing.T) {
	moved := &movingStore{recorded: records.Entry{Key: values.Key("cells"), Revision: "one"}, moves: 100}
	err := records.Change(context.Background(), moved, moved.recorded.Key, func(records.Entry) ([]byte, bool, error) {
		return []byte(`"changed"`), true, nil
	})
	if err == nil {
		t.Fatal("Change() of an entry it never wrote = nil")
	}
}
