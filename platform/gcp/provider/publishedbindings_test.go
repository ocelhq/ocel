package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func TestABindingPublishedBeforeAProjectsFirstDeployIsKeptInTheDatabaseItsOwnDatabaseOptionNames(t *testing.T) {
	p, _ := deployingNetworks(t)
	cachingProvider(t, p, true)
	ctx := context.Background()
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	store := variablestore.Store{KeyValues: keyValues{p}, Cipher: fake.NewCipher()}

	if _, err := store.SetBinding(ctx, scope, "", "pulumi", "orders", variablestore.BindingWrite{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("publishing shop's orders binding before its first deploy = %v, want it published", err)
	}
	recorded, found, err := ports.ReadProjectDatabaseRecord(ctx, p.records, scope.Tier, scope.Project)
	if err != nil || !found || recorded != ports.OwnDatabase("ocel", scope.Tier, scope.Project) {
		t.Errorf("shop keeps its records in %q (%t, %v), want its own database", recorded, found, err)
	}
	if err := p.ApplyPreflight(ctx, deployingProject("shop")); err != nil {
		t.Errorf("shop's first deploy after a binding was published = %v, want it to keep the database the binding was published into", err)
	}
}
