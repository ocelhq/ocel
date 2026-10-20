package ports

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func databaseOf(t *testing.T, store KeyValues, in records.Partition) string {
	t.Helper()
	_, collection, err := store.open(in)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(collection.Path, "/databases/")
	database, _, _ := strings.Cut(rest, "/")
	return database
}

func TestAProjectsValuesAreKeptInADatabaseOfTheProjectsOwn(t *testing.T) {
	isolated(t)
	store := KeyValues{Clients: &Clients{Namespace: "ocel", Project: "acme", Region: "us-central1", Endpoint: "http://127.0.0.1:1"}}
	shop := variablestore.ValuesPartition(variablestore.Scope{Project: "shop", Tier: environment.TierPreview})
	blog := variablestore.ValuesPartition(variablestore.Scope{Project: "blog", Tier: environment.TierPreview})
	stacks := records.Partition{Tier: environment.TierPreview, Root: stackrecords.RootStacks, Path: []string{"shop"}}

	if got, want := databaseOf(t, store, shop), ProjectDatabase("ocel", environment.TierPreview, "shop"); got != want {
		t.Errorf("shop's values are kept in %q, want %q", got, want)
	}
	if databaseOf(t, store, shop) == databaseOf(t, store, blog) {
		t.Errorf("shop and blog keep their values in one database, and a condition can grant an app no less than a whole database")
	}
	if got, want := databaseOf(t, store, stacks), RecordsDatabase("ocel", environment.TierPreview); got != want {
		t.Errorf("shop's stack records are kept in %q, want the preview tier's own %q", got, want)
	}
}

func TestAProjectDatabaseIsNamedUnderItsTiersPrefixAndFitsWhatFirestoreTakes(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for named, prefix := range map[string]string{
			ProjectDatabase(provider19, tier, "a-project-slug-that-runs-long"):         ProjectDatabasePrefix(provider19, tier),
			ProjectWritableDatabase(provider19, tier, "a-project-slug-that-runs-long"): ProjectWritableDatabasePrefix(provider19, tier),
		} {
			if !strings.HasPrefix(named, prefix) || len(named) > 63 {
				t.Errorf("%s database %q, want it under %q and at most 63 characters", tier, named, prefix)
			}
		}
	}
	prefixes := []string{
		ProjectDatabasePrefix("ocel", environment.TierProduction), ProjectDatabasePrefix("ocel", environment.TierPreview),
		ProjectWritableDatabasePrefix("ocel", environment.TierProduction), ProjectWritableDatabasePrefix("ocel", environment.TierPreview),
	}
	for i, prefix := range prefixes {
		for j, other := range prefixes {
			if i != j && strings.HasPrefix(other, prefix) {
				t.Errorf("the prefix %q also names the databases under %q, so a condition on it reaches them", prefix, other)
			}
		}
	}
}

const provider19 = "nineteen-characters"
