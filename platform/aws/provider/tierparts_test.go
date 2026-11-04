package aws

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedparts"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier"
	cloudfrontpart "github.com/ocelhq/ocel/platform/aws/provider/tier/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

func TestRemovingTheLastPartInTheNetworkPlansTheNetworksTakedownToo(t *testing.T) {
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierProduction, sharedparts.NetworkPart, stackrecords.BootstrapPart{
		WrittenBy: "1.0.0",
		Resources: []provider.PhysicalResource{{Kind: "AWS::EC2::VPC", ID: "vpc-ocel"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, consumer := range []provider.StackRef{containers.StackRef(environment.TierProduction), bastion.ConsumerRef(environment.TierProduction)} {
		if _, err := store.Write(ctx, records.Entry{Key: network.Shared.ConsumerAt(consumer), Value: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	consumers := map[string]string{awsports.ContainersSlug: sharedparts.ContainersPart, sharedparts.FunctionNetworkSlug: sharedparts.FunctionNetworkPart, bastion.ConsumerSlug: bastion.Part}

	group, err := planNetworkRetiredWith(ctx, store, "us-east-1", consumers, environment.TierProduction, []string{bastion.Part})
	if err != nil || group != nil {
		t.Errorf("planNetworkRetiredWith(bastion) = %+v, %v, want nothing: the containers part still runs in the network", group, err)
	}
	group, err = planNetworkRetiredWith(ctx, store, "us-east-1", consumers, environment.TierProduction, []string{bastion.Part, sharedparts.ContainersPart})
	if err != nil || group == nil || group.Part != sharedparts.NetworkPart || group.Action != provider.ActionDelete || len(group.Changes) != 1 {
		t.Errorf("planNetworkRetiredWith(bastion, containers) = %+v, %v, want the network's deletion: its last parts leave in this run", group, err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: network.Shared.ConsumerAt(provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("shop")}), Value: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if group, err := planNetworkRetiredWith(ctx, store, "us-east-1", consumers, environment.TierProduction, []string{bastion.Part, sharedparts.ContainersPart}); err != nil || group != nil {
		t.Errorf("planNetworkRetiredWith() = %+v, %v, want nothing while shop's database sits in the network", group, err)
	}
}

func partsOver(store records.Store) tier.Parts {
	p := NewProvider(control.Options{Region: "us-east-1"}, nil, aws.Config{Region: "us-east-1"}, defaultNamespace)
	return p.tierParts(tier.Records{KeyValues: store, WrittenBy: "1.0.0", Region: "us-east-1", Reads: &tier.Reads{}})
}

func TestADeployNeedsThePartsItsBuildsUse(t *testing.T) {
	t.Parallel()
	needed := partsOver(fake.NewKeyValues()).AddedFor([]buildoutput.Uses{{Revalidation: true, ImageOptimization: true}, {}}, edge.None).Names()

	want := []string{imageoptimizer.Part, taginvalidator.Part}
	if !slices.Equal(needed, want) {
		t.Errorf("AddedFor() = %v, want %v: web optimizes images and revalidates", needed, want)
	}
}

func TestADeployWhoseBuildsUseNeitherNeedsNoPart(t *testing.T) {
	t.Parallel()
	if needed := partsOver(fake.NewKeyValues()).AddedFor([]buildoutput.Uses{{}}, edge.None).Names(); len(needed) != 0 {
		t.Errorf("AddedFor() = %v, want none", needed)
	}
}

func TestAnAppWithNoBuildToReadIsPresumedToUseWhatItsFrameworkCan(t *testing.T) {
	t.Parallel()
	needed := partsOver(fake.NewKeyValues()).AddedFor([]buildoutput.Uses{buildoutput.PresumeUses("next")}, edge.None).Names()
	if want := []string{imageoptimizer.Part, taginvalidator.Part}; !slices.Equal(needed, want) {
		t.Errorf("AddedFor() = %v, want %v, as the app's serving presumes for a next app it cannot read the hosting of", needed, want)
	}
}

func TestADeployBehindCloudFrontAddsTheCloudFrontPartBesideWhatItsBuildsUse(t *testing.T) {
	t.Parallel()

	added := partsOver(fake.NewKeyValues()).AddedFor([]buildoutput.Uses{{ImageOptimization: true}}, cloudfront.Kind).Names()
	if want := []string{cloudfrontpart.Part, imageoptimizer.Part}; !slices.Equal(added, want) {
		t.Errorf("AddedFor() = %v, want %v in name order, the order a project's record of the parts it uses keeps", added, want)
	}
}

func TestThePreviewWildcardUsesThePartItsEdgeServesItThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for kind, name := range map[edge.Kind]string{cloudfront.Kind: cloudfrontpart.Part, apigateway.Kind: notfoundapi.Part} {
		store := fake.NewKeyValues()
		recorded, err := json.Marshal(stackrecords.Wildcard{BaseDomain: "pr.example.com", Edge: kind})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(ctx, records.Entry{Key: stackrecords.WildcardKey(environment.TierPreview), Value: recorded}); err != nil {
			t.Fatal(err)
		}
		for _, each := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
			if err := stackrecords.MarkBootstrapPartReady(ctx, store, each, name, stackrecords.BootstrapPart{WrittenBy: "1.0.0"}); err != nil {
				t.Fatal(err)
			}
		}
		part, found := partsOver(store).Named(name)
		if !found {
			t.Fatalf("tierParts() holds no %s", name)
		}

		preview, _, err := part.Describe(ctx, environment.TierPreview)
		if err != nil {
			t.Fatalf("Describe() = %v", err)
		}
		if !slices.Contains(preview.UsedBy, "*.pr.example.com") {
			t.Errorf("%s users = %v with the preview wildcard on the %s edge, want *.pr.example.com: removing it would leave the wildcard unanswered", name, preview.UsedBy, kind)
		}
		production, _, err := part.Describe(ctx, environment.TierProduction)
		if err != nil || len(production.UsedBy) != 0 {
			t.Errorf("production %s users = %v, %v, want none: the wildcard serves previews", name, production.UsedBy, err)
		}
	}
}

func TestTheContainerInfrastructureTheFunctionNetworkAndTheBastionAreOfferedForRemovalByName(t *testing.T) {
	t.Parallel()

	p := NewProvider(control.Options{}, nil, aws.Config{}, defaultNamespace)
	parts := p.tierParts(tier.Records{KeyValues: fake.NewKeyValues(), Reads: &tier.Reads{}})
	for _, name := range []string{sharedparts.ContainersPart, sharedparts.FunctionNetworkPart, bastion.Part} {
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
