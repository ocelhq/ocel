package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
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
