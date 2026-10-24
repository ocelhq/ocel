package variables

import (
	"context"
	"slices"
	"strings"
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
	keys      map[string]bool
	taken     []string
}

func (h *heldStores) stores() projectStores {
	return projectStores{
		keyExists: func(_ context.Context, name string) (bool, error) { return h.keys[name], nil },
		takeDatabase: func(_ context.Context, _ environment.Tier, id string) error {
			delete(h.databases, id)
			h.taken = append(h.taken, "database "+id)
			return nil
		},
		takeRuns: func(_ context.Context, tier environment.Tier, project string) error {
			h.taken = append(h.taken, "runs "+string(tier)+"/"+project)
			return nil
		},
		takeKey: func(_ context.Context, name string) error {
			delete(h.keys, name)
			h.taken = append(h.taken, "key "+name)
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
	held := &heldStores{
		databases: map[string]bool{database: true},
		keys:      map[string]bool{ports.ProjectKey(tier, project): true},
	}
	return engine, held
}

func plannedRemovals(t *testing.T, engine bootstrap.Engine, held *heldStores, project string) []string {
	t.Helper()
	changes, err := planRemoveProject(context.Background(), engine, held.stores(), environment.TierProduction, project)
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

func TestRemovingAProjectWithADatabaseOfItsOwnPlansThatDatabaseAndItsKey(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	own := ports.OwnDatabase("ocel", tier, "shop")
	engine, held := removing(t, "shop", own)

	want := []string{
		string(bootstrap.KindDatabase) + " " + own,
		string(bootstrap.KindKey) + " " + ports.ProjectKey(tier, "shop"),
	}
	if planned := plannedRemovals(t, engine, held, "shop"); !slices.Equal(planned, want) {
		t.Errorf("removing shop plans %v, want %v", planned, want)
	}
}

func TestRemovingAProjectThatSharesItsTiersDatabasePlansOnlyItsKey(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	engine, held := removing(t, "shop", ports.SharedDatabase("ocel", tier))

	want := []string{string(bootstrap.KindKey) + " " + ports.ProjectKey(tier, "shop")}
	if planned := plannedRemovals(t, engine, held, "shop"); !slices.Equal(planned, want) {
		t.Errorf("removing shop plans %v, want %v: the shared database stays for the tier's other projects", planned, want)
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
			if !slices.Contains(held.taken, wantTaken) || held.keys[ports.ProjectKey(tier, "shop")] {
				t.Errorf("removing shop took %v, want %q and its key", held.taken, wantTaken)
			}
			if name == "shared" && slices.ContainsFunc(held.taken, func(taken string) bool { return strings.HasPrefix(taken, "database ") }) {
				t.Errorf("removing shop took %v, which deletes the database the tier's other projects share", held.taken)
			}
			if _, found, err := ports.ReadProjectDatabaseRecord(context.Background(), engine.Records, tier, "shop"); err != nil || found {
				t.Errorf("the database of shop is still on record: %t, %v", found, err)
			}
			if changes, err := planRemoveProject(context.Background(), engine, held.stores(), tier, "shop"); err != nil || len(changes) != 0 {
				t.Errorf("removing shop again plans %v, %v, want nothing left to take", changes, err)
			}
		})
	}
}

func TestRemovingAProjectOrItsTierWithCloudKMSOffAsksForNoKey(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	server.ServicesOff = []string{"cloudkms.googleapis.com"}
	b := bootstrap.Engine{Clients: server.Open(t)}
	ctx := context.Background()
	tier := environment.TierProduction
	key := ports.ProjectKey(tier, "shop")
	stores := storesOf(b, nil)

	if held, err := stores.keyExists(ctx, key); err != nil || held {
		t.Errorf("keyExists(%s) with Cloud KMS off = %t, %v, want no key: none can be made while it is off", key, held, err)
	}
	if err := stores.takeKey(ctx, key); err != nil {
		t.Errorf("takeKey(%s) with Cloud KMS off = %v, want the project removed", key, err)
	}
	if err := takeProjectKeys(ctx, b, tier); err != nil {
		t.Errorf("takeProjectKeys() with Cloud KMS off = %v, want the tier removed", err)
	}
}
