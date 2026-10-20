package aws

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func TestEveryTierPartShapesWhatItHoldsAndPlansItsOwnTeardown(t *testing.T) {
	t.Parallel()

	for _, part := range partsOver(fake.NewKeyValues()) {
		if part.Shape == nil {
			t.Errorf("part %s shapes nothing, so its plan and its status cannot say what it costs", part.Name)
		}
		if part.Describe == nil {
			t.Errorf("part %s describes nothing, so status cannot show it", part.Name)
		}
		if part.Teardown == nil || part.Teardown.Plan == nil || part.Teardown.Remove == nil {
			t.Errorf("part %s has no complete teardown, so removing the tier either leaves it behind or deletes it unplanned", part.Name)
		}
		if part.Removal != nil && (part.Removal.Plan == nil || part.Removal.Remove == nil) {
			t.Errorf("part %s offers a removal it cannot both plan and perform", part.Name)
		}
	}
}

func TestARemovalOfTheNetworkThatAPartStillRunsInIsRefusedNamingIt(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	ctx := context.Background()
	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierProduction, network.Part, stackrecords.BootstrapPart{
		WrittenBy: "1.0.0", AddedBy: "shop",
		Resources: []provider.PhysicalResource{{Kind: "AWS::EC2::VPC", ID: "vpc-ocel"}},
	}); err != nil {
		t.Fatal(err)
	}
	held := network.Shared.ConsumerAt(provider.StackRef{Project: "ocel-containers", Tier: environment.TierProduction, Name: naming.InfraStack(string(environment.TierProduction))})
	if _, err := store.Write(ctx, records.Entry{Key: held, Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	part, found := partsOver(store).Named(network.Part)
	if !found || part.Removal == nil {
		t.Fatalf("no removable %s part", network.Part)
	}
	_, err := part.Removal.Plan(ctx, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "the containers part still runs") {
		t.Fatalf("Removal.Plan() = %v, want a refusal naming the containers part still in it: a VPC is not deleted while anything runs in it", err)
	}
}

func TestARemovalOfARecordedPartThatADeployStillUsesIsRefusedNamingTheDeploy(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	ctx := context.Background()
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, stackrecords.PartWork{
		Name: imageoptimizer.Part, WrittenBy: "1.0.0", Digest: "d", AddedBy: "shop",
		Make: func(context.Context) ([]provider.PhysicalResource, error) {
			return []provider.PhysicalResource{{Kind: "AWS::Lambda::Function", ID: "ocel-prod-optimizer"}}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: stackrecords.ProjectKey(environment.TierProduction, "shop"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.RecordPartsUsed(ctx, store, environment.TierProduction, "shop", []string{imageoptimizer.Part}); err != nil {
		t.Fatal(err)
	}

	part, _ := partsOver(store).Named(imageoptimizer.Part)
	_, err := part.Removal.Plan(ctx, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("Removal.Plan() = %v, want a refusal: shop's images are still served through it", err)
	}
}
