package fake_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestAnEdgeDescribesPermissionsOnlyOnceItIsGivenADocument(t *testing.T) {
	t.Parallel()

	edges := fake.NewEdges()
	relay := edges.Edge(fake.KindRelay)
	if relay.Hooks().DescribeCredentialPermissions != nil {
		t.Fatal("the relay edge describes permissions before it is given a document")
	}

	relay.DocumentsPermissions(edge.CredentialDocument{Heading: "relay token", Document: "scripts · edit"})
	documented, err := relay.Hooks().DescribeCredentialPermissions(edge.PurposeDeploy)
	if err != nil {
		t.Fatalf("DescribeCredentialPermissions err = %v", err)
	}
	if documented.Heading != "relay token" || documented.Document != "scripts · edit for deploy" {
		t.Errorf("document = %+v, want the relay token's scopes for the deploy purpose", documented)
	}
}

func TestAHostnameBoundAgainWithoutAnOriginIsServedByItsEdgeRatherThanTheOriginItWasForwardedTo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := fake.NewProvider(fake.Options{})
	relay, err := p.Edges().Open(fake.KindRelay, nil)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := relay.Reconcile(ctx, edge.StackSpec{Tier: environment.TierProduction, Slug: "shop"}, edge.StackState{})
	if err != nil {
		t.Fatal(err)
	}
	origin := fake.Origin(fake.RouterDirect)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", Origin: &origin}); err != nil {
		t.Fatalf("BindDomain(forwarded) = %v", err)
	}
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com"}); err != nil {
		t.Fatalf("BindDomain(served) = %v", err)
	}

	if serving, err := p.Liveness().ServingRouter(ctx, "shop.example.com"); err != nil || serving != fake.RouterRelay {
		t.Errorf("ServingRouter() = %q, %v; want %q: the binding no longer forwards to the origin", serving, err, fake.RouterRelay)
	}
}

func TestAnEdgeOpenedWithTunnelSetTunnelsToItsOriginAndRecordsWhatItServesOnTheEdgeTheTestsRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	edges := fake.NewEdges()
	tunneled, err := edges.Open(fake.KindRelay, provider.Options{"tunnel": true})
	if err != nil {
		t.Fatal(err)
	}
	addressed, err := edges.Open(fake.KindRelay, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !tunneled.Facts().TunnelsToOrigin || addressed.Facts().TunnelsToOrigin {
		t.Errorf("TunnelsToOrigin = %v with tunnel set and %v without, want only the one set to", tunneled.Facts().TunnelsToOrigin, addressed.Facts().TunnelsToOrigin)
	}

	stack, err := tunneled.Reconcile(ctx, edge.StackSpec{Tier: environment.TierProduction, Slug: "shop"}, edge.StackState{})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if bound := edges.Edge(fake.KindRelay).Bindings(); len(bound) != 1 || bound[0].Hostname != "shop.example.com" {
		t.Errorf("Bindings() = %+v, want the binding made through the tunneled edge", bound)
	}
}

func TestAnEdgeRefusesAnOptionItDoesNotTake(t *testing.T) {
	t.Parallel()

	if _, err := fake.NewEdges().Open(fake.KindRelay, provider.Options{"tunel": true}); err == nil {
		t.Fatal("Open() with a misspelt option = nil, want a refusal")
	}
}
