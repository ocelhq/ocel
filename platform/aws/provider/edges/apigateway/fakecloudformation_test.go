package apigateway

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

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
	var outputs []cfntypes.Output
	switch aws.ToString(in.StackName) {
	case coreStackName, previewCoreStackName:
		outputs = []cfntypes.Output{
			{OutputKey: aws.String("StateTableName"), OutputValue: aws.String(fakeStateTable)},
			{OutputKey: aws.String("EdgeBucketName"), OutputValue: aws.String(fakeEdgeBucket)},
		}
		if !f.noInvokeRole {
			outputs = append(outputs, cfntypes.Output{OutputKey: aws.String(account.OutputEdgeInvokeRoleARN), OutputValue: aws.String(fakeInvokeRole)})
		}
	default:
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName: in.StackName,
		Outputs:   outputs,
	}}}, nil
}
