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

func TestAValuePublishedBeforeAProjectsFirstDeployIsKeptInTheDatabaseItsOwnDatabaseOptionNames(t *testing.T) {
	p, _ := deployingNetworks(t)
	cachingProvider(t, p, true)
	ctx := context.Background()
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	store := variablestore.Store{KeyValues: keyValues{p}, Cipher: fake.NewCipher()}
	at := variablestore.Coordinate{Cell: variablestore.Cell{Key: "API_KEY"}}
	from := variablestore.Provenance{Publisher: "urn:pulumi:prod::infra::pulumi-nodejs:dynamic:Resource::ocel-variables-production", PublishedSecret: true}

	if _, err := store.SetPublished(ctx, scope, at, "k3y", from, 0); err != nil {
		t.Fatalf("publishing shop's API_KEY before its first deploy = %v, want it kept", err)
	}
	recorded, found, err := ports.ReadProjectDatabaseRecord(ctx, p.records, scope.Tier, scope.Project)
	if err != nil || !found || recorded != ports.OwnDatabase("ocel", scope.Tier, scope.Project) {
		t.Errorf("shop keeps its records in %q (%t, %v), want its own database", recorded, found, err)
	}
	if err := p.ApplyPreflight(ctx, deployingProject("shop")); err != nil {
		t.Errorf("shop's first deploy after a value was published = %v, want it to keep the database the value was published into", err)
	}
	if value, err := store.Get(ctx, scope, at, true); err != nil || value.Plaintext != "k3y" || value.Provenance.Publisher != from.Publisher {
		t.Errorf("API_KEY after the first deploy = %+v, %v, want the published value", value, err)
	}
}
