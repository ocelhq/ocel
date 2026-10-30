package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestANextServiceBehindAnEdgeThatPurgesByTagKeepsItsCacheTags(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	front, err := p.Edges().Open(albresources.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := routedNextSpec()
	spec.Edge = front

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := googletest.EnvOf(server.Created[0].Template.Containers[0])[edge.CacheTagPurgeVar]; got != "1" {
		t.Errorf("the Next service reads %s=%q, want 1: the alb clears a replaced release by its tag", edge.CacheTagPurgeVar, got)
	}
}

func TestANodeServiceBehindAnEdgeThatCachesResponsesIsToldItsRelease(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	front, err := p.Edges().Open(albresources.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := naming.NewReleaseToken("b1", "f1")
	spec := nextSpec()
	spec.Edge = front
	spec.Ref.Name.Release = token
	spec.App.Framework = buildoutput.FrameworkSvelteKit
	spec.App.Functions[0].Framework.Name = buildoutput.FrameworkSvelteKit

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	env := googletest.EnvOf(server.Created[0].Template.Containers[0])
	if env[edge.ReleaseTokenVar] != token.String() {
		t.Errorf("the service reads %s=%q, want %q", edge.ReleaseTokenVar, env[edge.ReleaseTokenVar], token)
	}
	if env[edge.EdgeCacheVar] != string(edge.EdgeCacheAll) {
		t.Errorf("the service reads %s=%q, want all: Cloud CDN stores whatever the response headers allow", edge.EdgeCacheVar, env[edge.EdgeCacheVar])
	}
}

func TestANextServiceBehindAnEdgeThatPurgesNothingByTagKeepsNoCacheTags(t *testing.T) {
	none, err := providerServing((&googletest.Run{}), t).Edges().Open(edge.None, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, front := range map[string]edge.Edge{"no edge": nil, "cloud run": none} {
		t.Run(name, func(t *testing.T) {
			server := &googletest.Run{}
			p := providerServing(server, t)
			publishGateOn(t, p, environment.TierProduction, nil)
			spec := routedNextSpec()
			spec.Edge = front

			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}

			if got, set := googletest.EnvOf(server.Created[0].Template.Containers[0])[edge.CacheTagPurgeVar]; set {
				t.Errorf("the Next service reads %s=%q, want it unset: nothing in front of it clears by tag", edge.CacheTagPurgeVar, got)
			}
		})
	}
}

func TestANextServiceBehindAnEdgeThatCachesResponsesIsToldItsEdgeStoresWhateverItsHeadersAllow(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	front, err := p.Edges().Open(albresources.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := routedNextSpec()
	spec.Edge = front

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := googletest.EnvOf(server.Created[0].Template.Containers[0])[edge.EdgeCacheVar]; got != string(edge.EdgeCacheAll) {
		t.Errorf("the Next service reads %s=%q, want all: Cloud CDN stores whatever the response headers allow", edge.EdgeCacheVar, got)
	}
}

func TestANextServiceBehindNoCachingEdgeIsToldNoEdgeStoresIt(t *testing.T) {
	none, err := providerServing((&googletest.Run{}), t).Edges().Open(edge.None, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, front := range map[string]edge.Edge{"no edge": nil, "cloud run": none} {
		t.Run(name, func(t *testing.T) {
			server := &googletest.Run{}
			p := providerServing(server, t)
			publishGateOn(t, p, environment.TierProduction, nil)
			spec := routedNextSpec()
			spec.Edge = front

			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}

			if got, set := googletest.EnvOf(server.Created[0].Template.Containers[0])[edge.EdgeCacheVar]; set {
				t.Errorf("the Next service reads %s=%q, want it unset: nothing in front of it stores a response", edge.EdgeCacheVar, got)
			}
		})
	}
}

func TestANodeServiceWithAnOcelRouteTableIsToldWhereItsImageHoldsIt(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := nextSpec()
	spec.App.Framework = buildoutput.FrameworkNode
	spec.App.Functions[0].Framework.Name = buildoutput.FrameworkNode
	spec.App.OcelRouteTable = &provider.RoutingSpec{RootFunction: "index"}

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got, want := googletest.EnvOf(server.Created[0].Template.Containers[0])[edge.OcelRouteTableVar], "/ocel/app/"+edge.OcelRouteTableFile; got != want {
		t.Errorf("the service reads %s=%q, want %q", edge.OcelRouteTableVar, got, want)
	}
}
