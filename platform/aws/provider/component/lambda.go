package component

import (
	"github.com/ocelhq/ocel/pkg/naming"
	cloudwatch "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	lambda "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	LambdaLogGroupPrefix   = "/aws/lambda/"
	LambdaLogFormat        = "Text"
	LambdaLogRetentionDays = 14
	LambdaNodeRuntime      = "nodejs24.x"
)

func LambdaLogGroupPrefixFor(base string) string {
	return LambdaLogGroupPrefix + base + naming.WordSeparator
}

func NewLambdaLogGroup(ctx *pulumi.Context, id, base string, kind naming.Kind, bundle string, tags map[string]string, opts ...pulumi.ResourceOption) (*cloudwatch.LogGroup, error) {
	return cloudwatch.NewLogGroup(ctx, id, &cloudwatch.LogGroupArgs{
		NamePrefix:      pulumi.String(LambdaLogGroupPrefixFor(base)),
		RetentionInDays: pulumi.Int(LambdaLogRetentionDays),
		Tags:            Tags(kind, bundle, tags),
	}, opts...)
}

func LambdaLogging(group *cloudwatch.LogGroup) lambda.FunctionLoggingConfigPtrInput {
	return &lambda.FunctionLoggingConfigArgs{
		LogFormat: pulumi.String(LambdaLogFormat),
		LogGroup:  group.Name,
	}
}
