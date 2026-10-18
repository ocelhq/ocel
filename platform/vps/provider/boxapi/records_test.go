package boxapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func writeEntry(t *testing.T, root string, key records.Key, value string) {
	t.Helper()
	path, err := PathOf(key)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(KeyValuesDir(root, key.Partition.Tier), path+EntrySuffix)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"revision":"0123456789abcdef0123456789abcdef","value":`+value+"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

var shop = records.Partition{Tier: environment.TierProduction, Root: variablestore.RootValues, Path: []string{"shop"}}

func TestTheEntriesAreReadOffTheTierTheyWereWrittenTo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	one := shop.Key("cells", "/", "DATABASE_URL", "*")
	two := shop.Key("cells", "/apps/web", "SESSION", "pr-7")
	writeEntry(t, root, one, `"one"`)
	writeEntry(t, root, two, `"two"`)
	store := KeyValues{Root: root}

	read, err := store.Read(context.Background(), one)
	if err != nil || string(read.Value) != `"one"` || read.Revision == "" {
		t.Fatalf("Read() = %+v, %v", read, err)
	}
	if _, err := store.Read(context.Background(), shop.Key("cells", "/", "MISSING", "*")); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() of nothing = %v, want %v", err, records.ErrNotFound)
	}
	listed, err := store.List(context.Background(), shop, "cells")
	if err != nil || len(listed) != 2 {
		t.Fatalf("List() = %v, %v, want both cells", listed, err)
	}
	for _, entry := range listed {
		if entry.Key.String() != one.String() && entry.Key.String() != two.String() {
			t.Errorf("List() named %s, which nothing wrote", entry.Key)
		}
	}
	other := records.Partition{Tier: shop.Tier, Root: shop.Root, Path: []string{"other"}}
	empty, err := store.List(context.Background(), other, "cells")
	if err != nil || len(empty) != 0 {
		t.Errorf("List() in another project = %v, %v, want nothing", empty, err)
	}
}

func TestAKeyRoundTripsThroughThePathAFileAnswersTo(t *testing.T) {
	t.Parallel()
	for _, key := range []records.Key{
		{Partition: records.Partition{Tier: environment.TierProduction, Root: conformance.Root, Path: []string{"TestOne/sub"}}, Path: []string{"leaf"}},
		shop.Key("cells", "/apps/web", "DATABASE_URL"),
		{Partition: records.Partition{Tier: environment.TierProduction, Root: ledger.Root, Path: []string{"a+b"}}, Path: []string{".hidden", ".."}},
	} {
		path, err := PathOf(key)
		if err != nil {
			t.Fatalf("PathOf(%s) = %v", key, err)
		}
		if strings.Contains(path, "/.") || strings.HasPrefix(path, ".") {
			t.Errorf("PathOf(%s) = %q, and a segment that starts a dot names something no entry is", key, path)
		}
		dir, rest, _ := strings.Cut(path, "/")
		if want, err := PartitionDir(key.Partition); err != nil || dir != want {
			t.Errorf("PathOf(%s) = %q, want it under the partition's own directory %q", key, path, want)
		}
		decoded, err := KeyOf(key.Partition, rest)
		if err != nil || decoded.String() != key.String() {
			t.Errorf("KeyOf(PathOf(%s)) = %s, %v", key, decoded, err)
		}
	}
}

func TestTwoPartitionsNeverShareADirectory(t *testing.T) {
	t.Parallel()
	one, err := PartitionDir(records.Partition{Tier: environment.TierProduction, Root: stackrecords.RootStacks, Path: []string{"a+b"}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := PartitionDir(records.Partition{Tier: environment.TierProduction, Root: stackrecords.RootStacks, Path: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Errorf("both partitions keep their entries in %q", one)
	}
}

func bootstrapped(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if err := os.MkdirAll(KeyValuesDir(root, tier), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const neverWritten records.Revision = "0123456789abcdef0123456789abcdef"

func TestTheBoxRecordsStoreConforms(t *testing.T) {
	conformance.RunStore(t, KeyValues{Root: bootstrapped(t)})
}

func TestAnEntryIsWrittenAsTheLineTheProviderReads(t *testing.T) {
	t.Parallel()
	root := bootstrapped(t)
	key := shop.Key("cells", "/", "DATABASE_URL", "*")
	revision, err := KeyValues{Root: root}.Write(context.Background(), records.Entry{Key: key, Value: []byte(`{ "url": "a<b&c",  "n": [1, 2] }`)})
	if err != nil {
		t.Fatal(err)
	}
	path, err := PathOf(key)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(KeyValuesDir(root, key.Partition.Tier), path+EntrySuffix)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"revision":"` + string(revision) + `","value":{"url":"a<b&c","n":[1,2]}}` + "\n"; string(raw) != want {
		t.Errorf("the entry's file holds %q, want %q", raw, want)
	}
	if len(revision) != 32 || strings.Trim(string(revision), "0123456789abcdef") != "" {
		t.Errorf("Write() = %q, want 32 lower-case hex digits", revision)
	}
	for _, written := range []string{file, filepath.Dir(file), filepath.Join(KeyValuesDir(root, key.Partition.Tier), ".lock")} {
		info, err := os.Lstat(written)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s is mode %04o, and nothing but its owner reads an entry", written, mode)
		}
	}
	if _, err := os.Lstat(file + ".staged"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staged file is still beside the entry: %v", err)
	}
}

func TestAWriteSucceedsOnlyWhenItExpectsTheCurrentRevision(t *testing.T) {
	t.Parallel()
	store := KeyValues{Root: bootstrapped(t)}
	ctx := context.Background()
	key := shop.Key("cells", "/", "SESSION", "*")

	first, err := store.Write(ctx, records.Entry{Key: key, Value: []byte(`"one"`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: key, Value: []byte(`"again"`)}); !errors.Is(err, records.ErrStale) {
		t.Errorf("a write expecting no entry over one = %v, want %v", err, records.ErrStale)
	}
	if _, err := store.Write(ctx, records.Entry{Key: key, Value: []byte(`"two"`), Revision: neverWritten}); !errors.Is(err, records.ErrStale) {
		t.Errorf("a write expecting a revision never written = %v, want %v", err, records.ErrStale)
	}
	second, err := store.Write(ctx, records.Entry{Key: key, Value: []byte(`"two"`), Revision: first})
	if err != nil || second == first {
		t.Fatalf("a write expecting the current revision = %q, %v, want a new revision", second, err)
	}
	read, err := store.Read(ctx, key)
	if err != nil || string(read.Value) != `"two"` || read.Revision != second {
		t.Errorf("Read() = %+v, %v, want the second write", read, err)
	}
}

func TestOfManyWritersExpectingNoEntryExactlyOneWrites(t *testing.T) {
	t.Parallel()
	store := KeyValues{Root: bootstrapped(t)}
	key := shop.Key("claims", "one")
	const writers = 16
	results := make(chan error, writers)
	for range writers {
		go func() {
			_, err := store.Write(context.Background(), records.Entry{Key: key, Value: []byte(`true`)})
			results <- err
		}()
	}
	won := 0
	for range writers {
		err := <-results
		switch {
		case err == nil:
			won++
		case !errors.Is(err, records.ErrStale):
			t.Errorf("a losing writer saw %v, want %v", err, records.ErrStale)
		}
	}
	if won != 1 {
		t.Errorf("%d writers claimed the absent entry, want exactly 1", won)
	}
}

func TestAPairWhoseSecondEntryMovedLeavesTheFirstUntouched(t *testing.T) {
	t.Parallel()
	store := KeyValues{Root: bootstrapped(t)}
	ctx := context.Background()
	one, two := shop.Key("pair", "one"), shop.Key("pair", "two")
	oneRevision, err := store.Write(ctx, records.Entry{Key: one, Value: []byte(`"one"`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: two, Value: []byte(`"two"`)}); err != nil {
		t.Fatal(err)
	}

	err = store.WritePair(ctx,
		records.Entry{Key: one, Value: []byte(`"one-moved"`), Revision: oneRevision},
		records.Entry{Key: two, Value: []byte(`"two-moved"`), Revision: neverWritten})
	if !errors.Is(err, records.ErrStale) {
		t.Fatalf("WritePair() with a stale second entry = %v, want %v", err, records.ErrStale)
	}
	read, err := store.Read(ctx, one)
	if err != nil || string(read.Value) != `"one"` || read.Revision != oneRevision {
		t.Errorf("the first entry is %+v, %v, want it untouched by a pair that was refused", read, err)
	}
}

func TestRemovingAnAbsentEntryIsNotFoundAndAMovedOneIsStale(t *testing.T) {
	t.Parallel()
	store := KeyValues{Root: bootstrapped(t)}
	ctx := context.Background()
	key := shop.Key("cells", "/", "GONE", "*")
	if err := store.Remove(ctx, key, ""); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Remove() of an absent entry = %v, want %v", err, records.ErrNotFound)
	}
	revision, err := store.Write(ctx, records.Entry{Key: key, Value: []byte(`1`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, key, neverWritten); !errors.Is(err, records.ErrStale) {
		t.Errorf("Remove() expecting another revision = %v, want %v", err, records.ErrStale)
	}
	if err := store.Remove(ctx, key, revision); err != nil {
		t.Fatalf("Remove() expecting the current revision = %v", err)
	}
	if _, err := store.Read(ctx, key); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() after Remove() = %v, want %v", err, records.ErrNotFound)
	}
}

func TestRemovingTheLastEntryOfADirectoryRemovesEachDirectoryItLeavesEmpty(t *testing.T) {
	t.Parallel()
	root := bootstrapped(t)
	store := KeyValues{Root: root}
	ctx := context.Background()
	dir := KeyValuesDir(root, environment.TierProduction)
	revisions, err := store.CompareAndSet(ctx, environment.TierProduction, []Change{
		{Path: "p/one/two/leaf", Value: []byte(`1`)},
		{Path: "p/one/beside", Value: []byte(`2`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p/one/two/crashed.json.staged"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompareAndSet(ctx, environment.TierProduction, []Change{{Path: "p/one/two/leaf", Revision: revisions[0], Remove: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "p/one/two")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the emptied directory p/one/two is still there: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "p/one/beside.json")); err != nil {
		t.Errorf("the entry beside the removed one went too: %v", err)
	}
}

func TestAnEntryBehindASymlinkOrOutsideTheStoreIsRefused(t *testing.T) {
	t.Parallel()
	root := bootstrapped(t)
	store := KeyValues{Root: root}
	ctx := context.Background()
	dir := KeyValuesDir(root, environment.TierProduction)
	elsewhere := t.TempDir()
	target := filepath.Join(elsewhere, "target.json")
	if err := os.WriteFile(target, []byte(`{"revision":"`+string(neverWritten)+`","value":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "p"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "p", "linked.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "p", "through")); err != nil {
		t.Fatal(err)
	}

	for _, change := range []Change{
		{Path: "p/linked", Revision: neverWritten, Value: []byte(`2`)},
		{Path: "p/linked", Revision: neverWritten, Remove: true},
		{Path: "p/through/target", Revision: neverWritten, Value: []byte(`2`)},
		{Path: "p/through/fresh", Value: []byte(`2`)},
		{Path: "../escaped", Value: []byte(`2`)},
		{Path: "p/../../escaped", Value: []byte(`2`)},
		{Path: "/abs", Value: []byte(`2`)},
		{Path: "", Value: []byte(`2`)},
	} {
		_, err := store.CompareAndSet(ctx, environment.TierProduction, []Change{change})
		if err == nil || errors.Is(err, records.ErrStale) || errors.Is(err, records.ErrNotFound) {
			t.Errorf("CompareAndSet(%+v) = %v, want it refused", change, err)
		}
	}
	if _, err := store.ReadPath(ctx, environment.TierProduction, "p/linked"); err == nil {
		t.Error("ReadPath() followed a symlinked entry")
	}
	if raw, err := os.ReadFile(target); err != nil || !strings.Contains(string(raw), `"value":1}`) {
		t.Errorf("the file a symlink names holds %q, %v, want it untouched", raw, err)
	}
	if _, err := os.Lstat(filepath.Join(elsewhere, "fresh.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a write through a symlinked directory landed outside the store: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), "escaped.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a path climbing out of the store landed beside it: %v", err)
	}
}

func TestAChangeSetIsOneOrTwoChangesToDistinctEntries(t *testing.T) {
	t.Parallel()
	store := KeyValues{Root: bootstrapped(t)}
	ctx := context.Background()
	for _, changes := range [][]Change{
		nil,
		{{Path: "p/a", Value: []byte(`1`)}, {Path: "p/b", Value: []byte(`1`)}, {Path: "p/c", Value: []byte(`1`)}},
		{{Path: "p/a", Value: []byte(`1`)}, {Path: "p/a", Value: []byte(`2`)}},
		{{Path: "p/a"}},
		{{Path: "p/a", Value: []byte(`{`)}},
		{{Path: "p/a", Value: []byte(`1`), Remove: true}},
	} {
		if _, err := store.CompareAndSet(ctx, environment.TierProduction, changes); err == nil {
			t.Errorf("CompareAndSet(%+v) succeeded, want it refused", changes)
		}
	}
	if listed, err := store.ListPath(ctx, environment.TierProduction, "p", ""); err != nil || len(listed) != 0 {
		t.Errorf("a refused change set left %+v, %v", listed, err)
	}
}

func TestATierWithoutAStoreRefusesWritesUntilBootstrapMakesIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := shop.Key("cells", "/", "A", "*")
	_, err := KeyValues{Root: root}.Write(context.Background(), records.Entry{Key: key, Value: []byte(`1`)})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("Write() with no store = %v, want a %s refusal naming the bootstrap", err, refusal.CodeNotReady)
	}
	if _, err := (KeyValues{Root: root}).Read(context.Background(), key); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() with no store = %v, want %v", err, records.ErrNotFound)
	}

	linked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(linked, string(environment.TierProduction)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), KeyValuesDir(linked, environment.TierProduction)); err != nil {
		t.Fatal(err)
	}
	if _, err := (KeyValues{Root: linked}).Write(context.Background(), records.Entry{Key: key, Value: []byte(`1`)}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("Write() into a symlinked store = %v, want it refused", err)
	}
}
