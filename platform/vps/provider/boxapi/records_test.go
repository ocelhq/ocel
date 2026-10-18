package boxapi

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

var shop = records.Partition{Tier: environment.TierProduction, Root: variablestore.RootValues, Path: []string{"shop"}}

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
