package control

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

func TestTheControlAssemblyListsAStacksResourcesFromItsRecordWithoutAnEngine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))}
	function := provider.PhysicalResource{Kind: "AWS::Lambda::Function", ID: "shop-prod-web-entry", Location: "us-east-1"}
	if err := stackrecords.WriteResources(ctx, store, ref, stackrecords.Resources{Held: []provider.PhysicalResource{function}}); err != nil {
		t.Fatal(err)
	}

	listed, err := stacksWithoutEngine{keyValues: store}.Resources(ctx, provider.StackResources{Ref: ref, NewestRelease: true})
	if err != nil || !slices.Equal(listed, []provider.PhysicalResource{function}) {
		t.Errorf("Stacks().Resources() = %v, %v, want the resources the stack's provision recorded, read with no engine", listed, err)
	}
}
