package control

import (
	"context"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

type presentBastion struct{ absentBastion }

func (p *presentBastion) DescribeClusters(_ context.Context, in *ecs.DescribeClustersInput, _ ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	return &ecs.DescribeClustersOutput{Clusters: []ecstypes.Cluster{{
		ClusterName: aws.String(in.Clusters[0]),
		Status:      aws.String("ACTIVE"),
		Tags:        []ecstypes.Tag{{Key: aws.String("ocel:managed-by"), Value: aws.String("ocel")}},
	}}}, nil
}

func describeParts(t *testing.T, b Bootstrap) map[string]provider.BootstrapPart {
	t.Helper()
	described, err := b.Describe(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	parts, err := b.DescribeParts(context.Background(), described)
	if err != nil {
		t.Fatalf("DescribeParts: %v", err)
	}
	if len(parts) == 0 || parts[0].Name != provider.BasePart {
		t.Fatalf("DescribeParts() = %+v, want the base first", parts)
	}
	named := map[string]provider.BootstrapPart{}
	for _, part := range parts {
		named[part.Name] = part
	}
	return named
}

func TestAnAWSBootstrapsBaseHoldsTheResourcesOfItsCoreStack(t *testing.T) {
	t.Parallel()
	b := installedBootstrapper(t, environment.TierProduction)
	core, err := defaultNamespace.StackNameFor(environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	b.CFN.(*teardownCFN).resources = map[string][]cfntypes.StackResourceSummary{core: {
		{LogicalResourceId: aws.String("StateBucket"), PhysicalResourceId: aws.String("ocel-state"), ResourceType: aws.String("AWS::S3::Bucket")},
		{LogicalResourceId: aws.String("StateTable"), PhysicalResourceId: aws.String("ocel-state-table"), ResourceType: aws.String("AWS::DynamoDB::Table")},
	}}

	base := describeParts(t, b)[provider.BasePart]

	want := []provider.PartResource{{Kind: "AWS::S3::Bucket", ID: "ocel-state"}, {Kind: "AWS::DynamoDB::Table", ID: "ocel-state-table"}}
	if !reflect.DeepEqual(base.Resources, want) {
		t.Errorf("base resources = %+v, want %+v", base.Resources, want)
	}
	if base.Shape == nil || len(base.Shape.Resources) == 0 {
		t.Errorf("base shape = %+v, want what the core stack's template prices", base.Shape)
	}
}

func TestAnAWSBootstrapReportsItsBastionOnlyOnceAPortForwardHasMadeIt(t *testing.T) {
	t.Parallel()
	b := installedBootstrapper(t, environment.TierProduction)
	if _, raised := describeParts(t, b)["bastion"]; raised {
		t.Fatal("DescribeParts() reports a bastion no port forward made")
	}

	b.Bastion.ECS = &presentBastion{}

	got, raised := describeParts(t, b)["bastion"]
	if !raised || len(got.Resources) == 0 || got.Resources[0].ID != bastion.NameFor(environment.TierProduction) {
		t.Errorf("bastion = %+v, %v, want it reported with its cluster", got, raised)
	}
}
