package aws

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
)

type partsRemoved struct{ names []string }

func (r *partsRemoved) removable(name string) tier.Part {
	return tier.Part{
		Name:    name,
		Summary: imageoptimizer.Summary,
		Removal: &tier.Removal{
			Plan: func(context.Context, environment.Tier) (*provider.ChangeGroup, error) {
				return tier.Deletion(name, "", nil), nil
			},
			Remove: func(context.Context, environment.Tier, progress.Log) error {
				r.names = append(r.names, name)
				return nil
			},
		},
	}
}

func removingParts(t *testing.T) (tierBootstrap, *fake.Bootstrap, *partsRemoved) {
	t.Helper()
	inner := fake.NewBootstrap()
	if err := inner.Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction}, nil); err != nil {
		t.Fatal(err)
	}
	store := fake.NewKeyValues()
	if err := stackrecords.MarkBootstrapPartReady(context.Background(), store, environment.TierProduction, imageoptimizer.Part, stackrecords.BootstrapPart{WrittenBy: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	removed := &partsRemoved{}
	return tierBootstrap{
		Bootstrap: inner,
		forget:    func() {},
		key:       func(context.Context, environment.Tier) (string, error) { return "", nil },
		keyValues: store,
		parts:     tier.Parts{removed.removable(imageoptimizer.Part)},
	}, inner, removed
}

func TestTheBootstrapOffersThePartsADeployAddsBesideItsOwn(t *testing.T) {
	t.Parallel()

	b, inner, _ := removingParts(t)
	parts := b.Parts()
	if !slices.ContainsFunc(parts, func(part provider.Part) bool { return part.Name == imageoptimizer.Part && len(part.Edges) == 0 }) {
		t.Errorf("Parts() = %v, want %s offered, fronting no edge: a deploy adds it, so no edge requires it of the bootstrap", parts, imageoptimizer.Part)
	}
	if len(parts) != len(inner.Parts())+1 {
		t.Errorf("Parts() = %v, want the CloudFormation bootstrap's own parts kept beside it", parts)
	}
}

func TestRemovingAPartADeployAddedPlansItsDeletionApartFromTheStacks(t *testing.T) {
	t.Parallel()

	b, _, _ := removingParts(t)
	plan, err := b.Plan(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{imageoptimizer.Part, fake.PartCache}})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	at := slices.IndexFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Part == imageoptimizer.Part })
	if at < 0 || plan.Groups[at].Action != provider.ActionDelete {
		t.Fatalf("Plan() = %+v, want a group deleting %s", plan.Groups, imageoptimizer.Part)
	}
	for _, group := range plan.Groups[at+1:] {
		if group.Part == imageoptimizer.Part {
			t.Errorf("Plan() shows %s twice: %+v", imageoptimizer.Part, plan.Groups)
		}
	}
	if !slices.ContainsFunc(plan.Groups, func(group provider.ChangeGroup) bool { return group.Part == fake.PartCache }) {
		t.Errorf("Plan() = %+v, want the stack part named beside it still planned by the CloudFormation bootstrap", plan.Groups)
	}
}

func TestRemovingAPartADeployAddedTakesItDownAndForgetsItsRecord(t *testing.T) {
	t.Parallel()

	b, inner, removed := removingParts(t)
	if err := b.Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{imageoptimizer.Part}}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if !slices.Equal(removed.names, []string{imageoptimizer.Part}) {
		t.Errorf("removed %v, want %s taken down", removed.names, imageoptimizer.Part)
	}
	applied := inner.Applied()
	if last := applied[len(applied)-1]; slices.Contains(last.Remove, imageoptimizer.Part) {
		t.Errorf("the CloudFormation bootstrap was asked to remove %v, and it holds no %s stack", last.Remove, imageoptimizer.Part)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(context.Background(), b.keyValues, environment.TierProduction, imageoptimizer.Part); err != nil || present {
		t.Errorf("ReadBootstrapPart(%s) = %v, %v, want the record gone, so the run's repair does not make it again", imageoptimizer.Part, present, err)
	}
}

func TestTheContainerInfrastructureTheFunctionNetworkAndTheBastionAreOfferedForRemovalByName(t *testing.T) {
	t.Parallel()

	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	parts := p.tierParts(tier.Records{KeyValues: fake.NewKeyValues(), Reads: &tier.Reads{}})
	for _, name := range []string{containers.Part, functionnetwork.Part, bastion.Part} {
		each, found := parts.Named(name)
		if !found || each.Removal == nil {
			t.Errorf("tierParts() offers no %s for removal, want `ocel bootstrap <tier> --remove %s` to take it down", name, name)
			continue
		}
		if each.Summary == "" || each.Removal.Plan == nil || each.Removal.Remove == nil {
			t.Errorf("%s is offered as %+v, want its summary, no edge, and a way to plan and take it down", name, each.Offer())
		}
	}
}
