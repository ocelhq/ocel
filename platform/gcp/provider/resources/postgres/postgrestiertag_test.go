package postgres

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestADatabasesCloudSQLInstanceCarriesItsTiersTagOnceHoweverOftenItIsProvisioned(t *testing.T) {
	h := servingPostgres(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(h.instances.clients)

	h.provision(t, "16")
	h.provision(t, "16")

	instance := h.server.Created[0].Name
	if got := tags.Bound(h.instances.clients.SQLInstanceTagged(instance).Name); !slices.Equal(got, []string{tags.ValueOf(environment.TierProduction)}) {
		t.Errorf("Cloud SQL instance %s carries %v, want the production value once", instance, got)
	}
}

func TestRemovingADatabaseRemovesItsInstancesTierTagBeforeTheInstance(t *testing.T) {
	h := servingPostgres(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(h.instances.clients)
	binding := h.provision(t, "16")
	instance := h.server.Created[0].Name

	if err := h.instances.remove(context.Background(), provider.Binding{Name: binding.Name}, nil); err != nil {
		t.Fatal(err)
	}
	if got := tags.Bound(h.instances.clients.SQLInstanceTagged(instance).Name); len(got) != 0 {
		t.Errorf("the removed instance %s left %v bound", instance, got)
	}
}

func TestADatabaseWhoseTierTagHasNotYetReachedTheCredentialIsResizedOnceItHas(t *testing.T) {
	h := servingPostgres(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(h.instances.clients)
	h.provision(t, "17")
	instance := h.server.Created[0].Name
	h.server.Instances[instance].Settings.Tier = "db-f1-micro"
	h.server.PatchesDenied = 2

	h.provision(t, "17")

	if len(h.server.Patched) != 1 || h.server.Instances[instance].Settings.Tier != "db-custom-1-3840" {
		t.Errorf("Cloud SQL was patched %d times to %s, want one resize once the tier's grants reached the instance", len(h.server.Patched), h.server.Instances[instance].Settings.Tier)
	}
}
