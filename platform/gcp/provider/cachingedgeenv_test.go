package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func TestANextServiceBehindAnEdgeThatPurgesByTagKeepsItsCacheTags(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	front, err := p.Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := routedNextSpec()
	spec.Edge = front

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := envOf(server.created[0].Template.Containers[0])[edge.CacheTagPurgeVar]; got != "1" {
		t.Errorf("the Next service reads %s=%q, want 1: the alb clears a replaced release by its tag", edge.CacheTagPurgeVar, got)
	}
}

func TestANodeServiceBehindAnEdgeThatCachesResponsesIsToldItsReleaseAndItsCacheRules(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	front, err := p.Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := naming.NewReleaseToken("b1", "f1")
	rules := []edge.CacheRule{{Path: "/blog/*", Revalidate: 60, Expire: 600}}
	spec := nextSpec()
	spec.Edge = front
	spec.Ref.Name.Release = token
	spec.App.Framework = buildoutput.FrameworkSvelteKit
	spec.App.Functions[0].Framework.Name = buildoutput.FrameworkSvelteKit
	spec.App.Cache = rules

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	env := envOf(server.created[0].Template.Containers[0])
	if env[edge.ReleaseTokenVar] != token.String() {
		t.Errorf("the service reads %s=%q, want %q", edge.ReleaseTokenVar, env[edge.ReleaseTokenVar], token)
	}
	if want := edge.CacheRulesVariable(rules); env[edge.CacheRulesVar] != want {
		t.Errorf("the service reads %s=%q, want %q", edge.CacheRulesVar, env[edge.CacheRulesVar], want)
	}
	if env[edge.OriginFreshOnlyVar] != "1" {
		t.Errorf("the service reads %s=%q, want 1: it sits behind a cache", edge.OriginFreshOnlyVar, env[edge.OriginFreshOnlyVar])
	}
}

func TestANextServiceBehindAnEdgeThatPurgesNothingByTagKeepsNoCacheTags(t *testing.T) {
	none, err := (&runServer{}).open(t).Edges().Open(edge.None, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, front := range map[string]edge.Edge{"no edge": nil, "cloud run": none} {
		t.Run(name, func(t *testing.T) {
			server := &runServer{}
			p := server.open(t)
			spec := routedNextSpec()
			spec.Edge = front

			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}

			if got, set := envOf(server.created[0].Template.Containers[0])[edge.CacheTagPurgeVar]; set {
				t.Errorf("the Next service reads %s=%q, want it unset: nothing in front of it clears by tag", edge.CacheTagPurgeVar, got)
			}
		})
	}
}

func TestANextServiceBehindAnEdgeThatCachesResponsesServesOnlyFreshPages(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	front, err := p.Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := routedNextSpec()
	spec.Edge = front

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := envOf(server.created[0].Template.Containers[0])[edge.OriginFreshOnlyVar]; got != "1" {
		t.Errorf("the Next service reads %s=%q, want 1: Cloud CDN revalidates a stale page itself", edge.OriginFreshOnlyVar, got)
	}
}

func TestANextServiceBehindNoCachingEdgeServesStalePages(t *testing.T) {
	none, err := (&runServer{}).open(t).Edges().Open(edge.None, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, front := range map[string]edge.Edge{"no edge": nil, "cloud run": none} {
		t.Run(name, func(t *testing.T) {
			server := &runServer{}
			p := server.open(t)
			spec := routedNextSpec()
			spec.Edge = front

			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}

			if got, set := envOf(server.created[0].Template.Containers[0])[edge.OriginFreshOnlyVar]; set {
				t.Errorf("the Next service reads %s=%q, want it unset: nothing in front of it revalidates a stale page", edge.OriginFreshOnlyVar, got)
			}
		})
	}
}
