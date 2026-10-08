package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func TestPurgingAProjectsValuesLeavesNothingOfItOnTheBox(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	here := newHelperHere(t)
	store := variablestore.Store{KeyValues: boxstore.NewKeyValues(here), Cipher: fake.NewCipher()}
	purged := variablestore.Scope{Project: "j-manual-express", Tier: environment.TierProduction}
	shared := variablestore.Scope{Project: "shared", Tier: environment.TierProduction}
	consumer := variablestore.Scope{Project: "consumer", Tier: environment.TierProduction}
	at := func(key string) variablestore.Coordinate {
		return variablestore.Coordinate{Cell: variablestore.Cell{Folder: "/", Key: key}, Environment: "production"}
	}
	tierWide := func(key string) variablestore.Coordinate {
		return variablestore.Coordinate{Cell: variablestore.Cell{Folder: "/", Key: key}}
	}
	for _, key := range []string{"DATABASE_URL", "STRIPE_KEY"} {
		if _, err := store.Set(ctx, purged, at(key), "v1", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Set(ctx, purged, at("STRIPE_KEY"), "v2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, purged, at("DATABASE_URL"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, shared, tierWide("SENTRY_DSN"), "dsn", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, purged, tierWide("SENTRY_DSN"),
		variablestore.Target{Project: shared.Project, Cell: variablestore.Cell{Folder: "/", Key: "SENTRY_DSN"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, purged, tierWide("REGION"), "eu", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, consumer, tierWide("REGION"),
		variablestore.Target{Project: purged.Project, Cell: variablestore.Cell{Folder: "/", Key: "REGION"}}); err != nil {
		t.Fatal(err)
	}

	root := keyValuesDir(t, here.root)
	partitions := map[string]keyvalue.Partition{
		"the project's values":                     variablestore.ValuesPartition(purged),
		"what references the project":              variablestore.ReferencesPartition(purged),
		"the index of the project's own reference": variablestore.ReferencesPartition(shared),
	}
	dirs := map[string]string{}
	for what, partition := range partitions {
		dir, err := live.PartitionDir(partition)
		if err != nil {
			t.Fatal(err)
		}
		dirs[what] = filepath.Join(root, dir)
		if _, err := os.Stat(dirs[what]); err != nil {
			t.Fatalf("%s was never written to %s: %v", what, dirs[what], err)
		}
	}

	if _, err := store.Purge(ctx, purged); err != nil {
		t.Fatal(err)
	}

	for what, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s, %s, is still on the box after the project's values were purged (%v)", what, dir, err)
		}
	}
	if got, err := store.Get(ctx, shared, tierWide("SENTRY_DSN"), true); err != nil || got.Plaintext != "dsn" {
		t.Errorf("shared's SENTRY_DSN reads %q (%v) after another project was purged, want dsn", got.Plaintext, err)
	}
}
