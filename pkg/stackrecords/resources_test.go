package stackrecords_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestAStacksRecordedResourcesAreReadWithItsWorkersOnlyForTheNewestRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))}
	function := provider.PhysicalResource{Kind: "AWS::Lambda::Function", ID: "web-fn", Location: "us-east-1"}
	mapping := provider.PhysicalResource{Kind: "AWS::Lambda::EventSourceMapping", ID: "web-worker", Location: "us-east-1"}
	if err := stackrecords.WriteResources(ctx, store, ref, stackrecords.Resources{Held: []provider.PhysicalResource{function}, Workers: []provider.PhysicalResource{mapping}}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{Kind: provider.StackApp, App: "web"}); err != nil {
		t.Fatal(err)
	}

	held, err := stackrecords.ReadResources(ctx, store, provider.StackResources{Ref: ref})
	if err != nil || !slices.Equal(held, []provider.PhysicalResource{function}) {
		t.Errorf("ReadResources() = %v, %v, want the stack's own resources", held, err)
	}
	newest, err := stackrecords.ReadResources(ctx, store, provider.StackResources{Ref: ref, NewestRelease: true})
	if err != nil || !slices.Equal(newest, []provider.PhysicalResource{function, mapping}) {
		t.Errorf("ReadResources(newest) = %v, %v, want its resources and its workers'", newest, err)
	}
	listed, err := stackrecords.List(ctx, store, ref.Tier, ref.Project)
	if err != nil || len(listed) != 1 {
		t.Errorf("List() = %v, %v, want the one stack, its resources not read as a stack", listed, err)
	}

	if err := stackrecords.Forget(ctx, store, ref.Tier, ref.Project, ref.Name); err != nil {
		t.Fatal(err)
	}
	if forgotten, err := stackrecords.ReadResources(ctx, store, provider.StackResources{Ref: ref}); err != nil || forgotten != nil {
		t.Errorf("ReadResources() after Forget = %v, %v, want nothing left of a forgotten stack", forgotten, err)
	}
}
