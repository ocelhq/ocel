package apigateway

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

type fakeCFN struct {
	absent       bool
	noInvokeRole bool
}

func (f *fakeCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if f.absent {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	var tier environment.Tier
	switch aws.ToString(in.StackName) {
	case coreStackName:
		tier = environment.TierProduction
	case previewCoreStackName:
		tier = environment.TierPreview
	default:
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	outputs := []cfntypes.Output{
		{OutputKey: aws.String("StateTableName"), OutputValue: aws.String(fakeStateTable)},
		{OutputKey: aws.String("StateBucketName"), OutputValue: aws.String(defaultNamespace.BootstrapBucketName(tier, naming.StorageState, fakeBucketTag))},
		{OutputKey: aws.String("ArtifactBucketName"), OutputValue: aws.String(defaultNamespace.BootstrapBucketName(tier, naming.StorageArtifacts, fakeBucketTag))},
		{OutputKey: aws.String("EdgeBucketName"), OutputValue: aws.String(defaultNamespace.BootstrapBucketName(tier, naming.StorageEdge, fakeBucketTag))},
		{OutputKey: aws.String("CacheBucketName"), OutputValue: aws.String(defaultNamespace.BootstrapBucketName(tier, naming.StorageCache, fakeBucketTag))},
	}
	if !f.noInvokeRole {
		outputs = append(outputs, cfntypes.Output{OutputKey: aws.String(account.OutputEdgeInvokeRoleARN), OutputValue: aws.String(fakeInvokeRole)})
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName: in.StackName,
		Outputs:   outputs,
	}}}, nil
}
