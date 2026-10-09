package gcp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAGCPBootstrapStatusListsTheEdgesBootstrapPartsUnderItsFeature(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.parts = []edge.BootstrapPart{
		{Name: "ocel-edge-cache", Current: true},
		{Name: "ocel-isr-writer", Current: false},
	}

	stacks, err := b.edgeBootstrapStacks(context.Background(), surveyed(albFeature))
	if err != nil {
		t.Fatalf("edgeBootstrapStacks = %v", err)
	}
	kind := string(registry.front.Kind())
	want := []provider.BootstrapStack{
		{Name: kind + "/ocel-edge-cache", Feature: albFeature, Present: true, DigestCurrent: true},
		{Name: kind + "/ocel-isr-writer", Feature: albFeature, Present: true, DigestCurrent: false},
	}
	if !reflect.DeepEqual(stacks, want) {
		t.Errorf("edge stacks = %+v, want %+v", stacks, want)
	}
}

func TestAGCPBootstrapStatusReportsAnUndescribableEdgeAsUnreadableRatherThanFailingOrStale(t *testing.T) {
	t.Parallel()

	b, registry := fronting(t)
	registry.front.describe = errors.New("CLOUDFLARE_API_TOKEN is not set")

	stacks, err := b.edgeBootstrapStacks(context.Background(), surveyed(albFeature))
	if err != nil {
		t.Fatalf("edgeBootstrapStacks = %v, want the status to survive an edge it cannot read", err)
	}
	want := []provider.BootstrapStack{
		{Name: string(registry.front.Kind()) + "/bootstrap", Feature: albFeature, Present: true, ReadError: "CLOUDFLARE_API_TOKEN is not set"},
	}
	if !reflect.DeepEqual(stacks, want) {
		t.Errorf("edge stacks = %+v, want %+v", stacks, want)
	}
}
