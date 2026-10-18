package control

import (
	"context"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

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

func TestAnAWSBootstrapsBaseCarriesTheDigestItsCoreStackWasStampedWith(t *testing.T) {
	t.Parallel()
	b := installedBootstrapper(t, environment.TierProduction)
	core, err := defaultNamespace.StackNameFor(environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	b.CFN.(*teardownCFN).tags = map[string][]cfntypes.Tag{core: {{Key: aws.String(cfn.TagDigest), Value: aws.String("sha256:core")}}}

	if got := describeParts(t, b)[provider.BasePart].Digest; got != "sha256:core" {
		t.Errorf("base digest = %q, want the core stack's stamp sha256:core", got)
	}
}

func TestAnAWSBootstrapsPartsStillReadWhenItsStacksResourcesCannotBeListed(t *testing.T) {
	t.Parallel()
	b := installedBootstrapper(t, environment.TierProduction)
	b.CFN.(*teardownCFN).listing = &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: cloudformation:ListStackResources"}

	base := describeParts(t, b)[provider.BasePart]

	if base.Resources != nil {
		t.Errorf("base resources = %+v, want them unknown", base.Resources)
	}
	if base.Shape == nil || len(base.Shape.Resources) == 0 {
		t.Errorf("base shape = %+v, want the core stack still priced", base.Shape)
	}
}
