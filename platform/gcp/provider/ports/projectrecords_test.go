package ports

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func databaseOf(t *testing.T, store KeyValues, in records.Partition) string {
	t.Helper()
	_, collection, err := store.open(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(collection.Path, "/databases/")
	database, _, _ := strings.Cut(rest, "/")
	return database
}

func TestAProjectWithNoDatabaseOnRecordKeepsItsRecordsInTheDatabaseItsTierShares(t *testing.T) {
	t.Parallel()

	database, err := ReadRecordedDatabase(context.Background(), fake.NewKeyValues(), "ocel", environment.TierPreview, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if want := SharedDatabase("ocel", environment.TierPreview); database != want {
		t.Errorf("shop keeps its records in %q, want the preview tier's shared %q", database, want)
	}
}

func TestAProjectRecordedWithADatabaseOfItsOwnKeepsItsRecordsThere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	own := OwnDatabase("ocel", environment.TierPreview, "shop")
	value, err := ProjectDatabaseRecord(own)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: ProjectDatabaseKey(environment.TierPreview, "shop"), Value: value}); err != nil {
		t.Fatal(err)
	}

	database, err := ReadRecordedDatabase(ctx, store, "ocel", environment.TierPreview, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if database != own {
		t.Errorf("shop keeps its records in %q, want its own %q", database, own)
	}
	blog, err := ReadRecordedDatabase(ctx, store, "ocel", environment.TierPreview, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if blog != SharedDatabase("ocel", environment.TierPreview) {
		t.Errorf("blog keeps its records in %q, want the shared database: only shop has a database of its own", blog)
	}
}

func TestAProjectsValuesAreKeptInTheDatabaseTheRuntimeIsToldAndATiersRecordsInItsOwn(t *testing.T) {
	isolated(t)
	own := OwnDatabase("ocel", environment.TierPreview, "shop")
	store := KeyValues{Clients: &Clients{
		Namespace: "ocel", Project: "acme", Region: "us-central1", Endpoint: "http://127.0.0.1:1",
		ProjectDatabases: map[string]string{"shop": own, "blog": SharedDatabase("ocel", environment.TierPreview)},
	}}
	shop := variablestore.ValuesPartition(variablestore.Scope{Project: "shop", Tier: environment.TierPreview})
	blog := variablestore.ValuesPartition(variablestore.Scope{Project: "blog", Tier: environment.TierPreview})
	stacks := records.Partition{Tier: environment.TierPreview, Root: stackrecords.RootStacks, Path: []string{"shop"}}

	if got := databaseOf(t, store, shop); got != own {
		t.Errorf("shop's values are kept in %q, want its own %q", got, own)
	}
	if got, want := databaseOf(t, store, blog), SharedDatabase("ocel", environment.TierPreview); got != want {
		t.Errorf("blog's values are kept in %q, want the shared %q", got, want)
	}
	if got, want := databaseOf(t, store, stacks), RecordsDatabase("ocel", environment.TierPreview); got != want {
		t.Errorf("shop's stack records are kept in %q, want the preview tier's own %q", got, want)
	}
}

func TestEveryAppDatabaseIsNamedUnderItsTiersFenceAndFitsWhatFirestoreTakes(t *testing.T) {
	t.Parallel()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		own := OwnDatabase(provider19, tier, "a-project-slug-that-runs-long")
		if !strings.HasPrefix(own, OwnDatabasePrefix(provider19, tier)) || len(own) > 63 {
			t.Errorf("%s database %q, want it under %q and at most 63 characters", tier, own, OwnDatabasePrefix(provider19, tier))
		}
		if shared := SharedDatabase(provider19, tier); len(shared) > 63 || strings.HasPrefix(shared, OwnDatabasePrefix(provider19, tier)) {
			t.Errorf("%s shared database %q, want at most 63 characters and outside the prefix of databases a project owns", tier, shared)
		}
	}
	names := []string{
		OwnDatabasePrefix("ocel", environment.TierProduction), OwnDatabasePrefix("ocel", environment.TierPreview),
		SharedDatabase("ocel", environment.TierProduction), SharedDatabase("ocel", environment.TierPreview),
	}
	for i, name := range names {
		for j, other := range names {
			if i != j && strings.HasPrefix(other, name) {
				t.Errorf("%q also names %q, so a condition on it reaches that database", name, other)
			}
		}
	}
}

const provider19 = "nineteen-characters"
