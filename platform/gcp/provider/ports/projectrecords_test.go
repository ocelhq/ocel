package ports

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func databaseOf(t *testing.T, store KeyValues, in keyvalue.Partition) string {
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
	stacks := keyvalue.Partition{Tier: environment.TierPreview, Root: keyvalue.RootStacks, Path: []string{"shop"}}

	if got, want := databaseOf(t, store, shop), ProjectDatabase("ocel", environment.TierPreview, "shop"); got != want {
		t.Errorf("shop's values are kept in %q, want %q", got, want)
	}
	if databaseOf(t, store, shop) == databaseOf(t, store, blog) {
		t.Errorf("shop and blog keep their values in one database, and a condition can grant an app no less than a whole database")
	}
	if got := databaseOf(t, store, stacks); got != Database("ocel") {
		t.Errorf("shop's stack records are kept in %q, want the namespace's own %q", got, Database("ocel"))
	}
}

func TestAProjectDatabaseIsNamedUnderItsTiersPrefixAndFitsWhatFirestoreTakes(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		database := ProjectDatabase(provider19, tier, "a-project-slug-that-runs-long")
		if !strings.HasPrefix(database, ProjectDatabasePrefix(provider19, tier)) || len(database) > 63 {
			t.Errorf("ProjectDatabase(%s) = %q, want it under %q and at most 63 characters", tier, database, ProjectDatabasePrefix(provider19, tier))
		}
	}
	if strings.HasPrefix(ProjectDatabasePrefix("ocel", environment.TierProduction), ProjectDatabasePrefix("ocel", environment.TierPreview)) ||
		strings.HasPrefix(TaskDatabase("ocel", environment.TierProduction), ProjectDatabasePrefix("ocel", environment.TierProduction)) {
		t.Error("a tier's project database prefix also names another tier's or a part's database, so a condition on it reaches them")
	}
}

const provider19 = "nineteen-characters"
