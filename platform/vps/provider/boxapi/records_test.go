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

func TestTheEntriesAreReadOffTheTierTheHelperWritesAndNeverWritten(t *testing.T) {
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
	if _, err := store.Write(context.Background(), read); err == nil {
		t.Error("the box-side entries wrote something, and a deploy writes through the helper alone")
	}
	if err := store.Remove(context.Background(), one, read.Revision); err == nil {
		t.Error("the box-side entries removed something")
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
