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

func TestADatabasesCloudSQLInstanceIsCreatedCarryingItsTiersTagAndIsNotBoundAfter(t *testing.T) {
	h := servingPostgres(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(h.instances.clients)
	h.server.TierTags = tags

	h.provision(t, "16")

	sent, _ := h.server.Sent[0]["tags"].(map[string]any)
	if sent["acme-prod/ocel-tier"] != "production" {
		t.Errorf("the Cloud SQL instance was created with the tags %v, want acme-prod/ocel-tier: production", sent)
	}
	if tags.BindingsAsked != 0 {
		t.Errorf("a binding was asked for %d times after the instance was created carrying its tag, want none", tags.BindingsAsked)
	}
}

func TestADatabaseFoundWithoutItsTiersTagIsTouchedOnlyOnceTheTagAdmitsTheCredential(t *testing.T) {
	h := servingPostgres(t)
	h.provision(t, "17")
	instance := h.server.Created[0].Name
	h.server.Databases[instance] = nil
	tags := googletest.NewTieredTags(t)
	tags.Tagged(h.instances.clients)
	h.server.Unadmitted = 2

	h.provision(t, "17")

	if got := tags.Bound(h.instances.clients.SQLInstanceTagged(instance).Name); !slices.Equal(got, []string{tags.ValueOf(environment.TierProduction)}) {
		t.Errorf("Cloud SQL instance %s carries %v, want the production value", instance, got)
	}
	if !slices.Contains(h.server.Databases[instance], "ocel") {
		t.Errorf("the instance holds the databases %v once its tag admitted the credential, want ocel", h.server.Databases[instance])
	}
}
