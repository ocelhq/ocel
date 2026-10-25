package variables

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type heldStores struct {
	databases map[string]bool
	taken     []string
}

func (h *heldStores) stores() projectStores {
	return projectStores{
		takeDatabase: func(_ context.Context, _ environment.Tier, id string) error {
			delete(h.databases, id)
			h.taken = append(h.taken, "database "+id)
			return nil
		},
		takeRuns: func(_ context.Context, tier environment.Tier, project string) error {
			h.taken = append(h.taken, "runs "+string(tier)+"/"+project)
			return nil
		},
	}
}

func removing(t *testing.T, project, database string) (bootstrap.Engine, *heldStores) {
	t.Helper()
	engine := bootstrap.Engine{
		Clients: &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Region: "europe-west1"},
		Records: fake.NewKeyValues(),
	}
	tier := environment.TierProduction
	value, err := ports.ProjectDatabaseRecord(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Records.Write(context.Background(), records.Entry{Key: ports.ProjectDatabaseKey(tier, project), Value: value}); err != nil {
		t.Fatal(err)
	}
	held := &heldStores{databases: map[string]bool{database: true}}
	return engine, held
}

func plannedRemovals(t *testing.T, engine bootstrap.Engine, project string) []string {
	t.Helper()
	changes, err := planRemoveProject(context.Background(), engine, environment.TierProduction, project)
	if err != nil {
		t.Fatal(err)
	}
	var planned []string
	for _, change := range changes {
		if change.Action != provider.ActionDelete {
			t.Errorf("%s %s is planned to %s, want deleted", change.Kind, change.Name, change.Action)
		}
		planned = append(planned, change.Kind+" "+change.Name)
	}
	return planned
}

func TestRemovingAProjectWithADatabaseOfItsOwnPlansThatDatabaseAlone(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	own := ports.OwnDatabase("ocel", tier, "shop")
	engine, _ := removing(t, "shop", own)

	want := []string{string(bootstrap.KindDatabase) + " " + own}
	if planned := plannedRemovals(t, engine, "shop"); !slices.Equal(planned, want) {
		t.Errorf("removing shop plans %v, want %v: its values were sealed under the tier key, which the tier's other projects still seal under", planned, want)
	}
}

func TestRemovingAProjectThatSharesItsTiersDatabasePlansNothing(t *testing.T) {
	t.Parallel()
	engine, _ := removing(t, "shop", ports.SharedDatabase("ocel", environment.TierProduction))

	if planned := plannedRemovals(t, engine, "shop"); len(planned) != 0 {
		t.Errorf("removing shop plans %v, want nothing: the shared database and the tier key stay for the tier's other projects", planned)
	}
}

func TestRemovingAProjectLeavesNothingOfItsRecordsBehind(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	for name, database := range map[string]string{
		"own":    ports.OwnDatabase("ocel", tier, "shop"),
		"shared": ports.SharedDatabase("ocel", tier),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			engine, held := removing(t, "shop", database)

			if err := removeProject(context.Background(), engine, held.stores(), tier, "shop", nil); err != nil {
				t.Fatal(err)
			}
			wantTaken := "database " + database
			if name == "shared" {
				wantTaken = "runs " + string(tier) + "/shop"
			}
			if !slices.Equal(held.taken, []string{wantTaken}) {
				t.Errorf("removing shop took %v, want %q alone", held.taken, wantTaken)
			}
			if name == "shared" && slices.ContainsFunc(held.taken, func(taken string) bool { return strings.HasPrefix(taken, "database ") }) {
				t.Errorf("removing shop took %v, which deletes the database the tier's other projects share", held.taken)
			}
			if _, found, err := ports.ReadProjectDatabaseRecord(context.Background(), engine.Records, tier, "shop"); err != nil || found {
				t.Errorf("the database of shop is still on record: %t, %v", found, err)
			}
			if changes, err := planRemoveProject(context.Background(), engine, tier, "shop"); err != nil || len(changes) != 0 {
				t.Errorf("removing shop again plans %v, %v, want nothing left to take", changes, err)
			}
		})
	}
}

func TestRemovingAProjectAsksGoogleForNoKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tier := environment.TierProduction
	var mu sync.Mutex
	var asked []string
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(google.Close)
	b := bootstrap.Engine{Clients: googletest.Clients(google.URL), Records: fake.NewKeyValues()}
	value, err := ports.ProjectDatabaseRecord(ports.SharedDatabase("ocel", tier))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Records.Write(ctx, records.Entry{Key: ports.ProjectDatabaseKey(tier, "shop"), Value: value}); err != nil {
		t.Fatal(err)
	}

	if changes, err := PlanRemoveProject(ctx, b, tier, "shop"); err != nil || len(changes) != 0 {
		t.Errorf("PlanRemoveProject() = %+v, %v, want nothing planned: shop's values were sealed under the tier key, which stays", changes, err)
	}
	noRuns := func(context.Context, environment.Tier, string) error { return nil }
	if err := RemoveProject(ctx, b, noRuns, tier, "shop", nil); err != nil {
		t.Errorf("RemoveProject() = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 0 {
		t.Errorf("removing shop asked Google %q, want nothing: no key of shop's is read or scheduled for destruction", asked)
	}
}
