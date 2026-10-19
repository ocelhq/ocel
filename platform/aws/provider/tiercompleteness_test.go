package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/vpcclients"
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

func TestTheFirstPlanOfATierWithADatabasePlansTheVPCClientsGroup(t *testing.T) {
	t.Parallel()

	part, found := partsOver(fake.NewKeyValues()).Named(vpcclients.Part)
	if !found || part.Added == nil {
		t.Fatalf("no %s part a deploy adds", vpcclients.Part)
	}
	group, err := part.Added.Plan(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Added.Plan() = %v", err)
	}
	if want := vpcclients.NameFor(string(defaultNamespace), environment.TierProduction); group == nil || len(group.Changes) != 1 || group.Changes[0].Name != want {
		t.Errorf("Added.Plan() = %+v, want the security group %s it creates", group, want)
	}
}

func TestARemovalOfARecordedPartThatADeployStillUsesIsRefusedNamingTheDeploy(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	ctx := context.Background()
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, stackrecords.PartWork{
		Name: vpcclients.Part, WrittenBy: "1.0.0", Digest: "d", AddedBy: "shop",
		Make: func(context.Context) ([]provider.PhysicalResource, error) {
			return []provider.PhysicalResource{{Kind: vpcclients.KindSecurityGroup, ID: "sg-1"}}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: stackrecords.ProjectKey(environment.TierProduction, "shop"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.RecordPartsUsed(ctx, store, environment.TierProduction, "shop", []string{vpcclients.Part}); err != nil {
		t.Fatal(err)
	}

	part, _ := partsOver(store).Named(vpcclients.Part)
	_, err := part.Removal.Plan(ctx, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("Removal.Plan() = %v, want a refusal: the databases of shop still admit only that group, and deleting it fails part-way", err)
	}
}
