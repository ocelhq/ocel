package variables

import (
	"context"
	"slices"
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
		databaseExists: func(_ context.Context, id string) (bool, error) { return h.databases[id], nil },
		keyExists:      func(_ context.Context, name string) (bool, error) { return h.keys[name], nil },
		takeDatabase: func(_ context.Context, _ environment.Tier, id string) error {
			delete(h.databases, id)
			h.taken = append(h.taken, "database "+id)
			return nil
		},
		takeKey: func(_ context.Context, name string) error {
			delete(h.keys, name)
			h.taken = append(h.taken, "key "+name)
			return nil
		},
	}
}

func removingShop(t *testing.T) (bootstrap.Engine, *heldStores) {
	t.Helper()
	engine := bootstrap.Engine{
		Clients: &gcloud.Clients{Names: names.New(provider.Namespace("ocel"), "acme-prod"), Region: "europe-west1"},
		Records: fake.NewKeyValues(),
	}
	tier := environment.TierProduction
	if _, err := engine.Records.Write(context.Background(), records.Entry{Key: writableDatabaseKey(tier, "shop"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	held := &heldStores{
		databases: map[string]bool{engine.Clients.ProjectDatabase(tier, "shop"): true, engine.Clients.ProjectWritableDatabase(tier, "shop"): true},
		keys:      map[string]bool{ports.ProjectKey(tier, "shop"): true},
	}
	return engine, held
}

func TestRemovingAProjectPlansItsRunsAndValuesDatabasesAndItsKey(t *testing.T) {
	t.Parallel()
	engine, held := removingShop(t)
	tier := environment.TierProduction

	changes, err := planRemoveProject(context.Background(), engine, held.stores(), tier, "shop")
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
	want := []string{
		string(bootstrap.KindDatabase) + " " + engine.Clients.ProjectWritableDatabase(tier, "shop"),
		string(bootstrap.KindDatabase) + " " + engine.Clients.ProjectDatabase(tier, "shop"),
		string(bootstrap.KindKey) + " " + ports.ProjectKey(tier, "shop"),
	}
	if !slices.Equal(planned, want) {
		t.Errorf("removing shop plans %v, want %v", planned, want)
	}
}

func TestRemovingAProjectLeavesNeitherOfItsDatabasesNorItsKeyBehind(t *testing.T) {
	t.Parallel()
	engine, held := removingShop(t)
	tier := environment.TierProduction
	held.databases[engine.Clients.ProjectWritableDatabase(tier, "shop")] = false

	if err := removeProject(context.Background(), engine, held.stores(), tier, "shop", nil); err != nil {
		t.Fatal(err)
	}
	if held.databases[engine.Clients.ProjectDatabase(tier, "shop")] || held.keys[ports.ProjectKey(tier, "shop")] {
		t.Errorf("after removing shop, databases %v and keys %v are left", held.databases, held.keys)
	}
	if entry, err := records.ReadOrEmpty(context.Background(), engine.Records, writableDatabaseKey(tier, "shop")); err != nil || len(entry.Value) > 0 {
		t.Errorf("the runs database of shop is still on record: %v, %v", entry, err)
	}
	if changes, err := planRemoveProject(context.Background(), engine, held.stores(), tier, "shop"); err != nil || len(changes) != 0 {
		t.Errorf("removing shop again plans %v, %v, want nothing left to take", changes, err)
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
	stores := storesOf(b)

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
