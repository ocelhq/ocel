package account

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

const outputAppBoundaryARN = "AppBoundaryArn"

func appBoundaryARNFor(ns Namespace, tier environment.Tier) string {
	return "arn:aws:iam::" + "${aws:PrincipalAccount}" + ":policy/" + ns.AppBoundaryNameFor(tier)
}

func appBoundaryActions() []string {
	return []string{
		"appsync:EventConnect",
		"appsync:EventPublish",
		"appsync:GetApi",
		"bedrock:InvokeModel",
		"bedrock:InvokeModelWithResponseStream",
		"cloudwatch:PutMetricData",
		"dynamodb:BatchGetItem",
		"dynamodb:BatchWriteItem",
		"dynamodb:ConditionCheckItem",
		"dynamodb:DeleteItem",
		"dynamodb:DescribeTable",
		"dynamodb:GetItem",
		"dynamodb:PutItem",
		"dynamodb:Query",
		"dynamodb:Scan",
		"dynamodb:TransactGetItems",
		"dynamodb:TransactWriteItems",
		"dynamodb:UpdateItem",
		"ec2:AssignPrivateIpAddresses",
		"ec2:CreateNetworkInterface",
		"ec2:DeleteNetworkInterface",
		"ec2:DescribeNetworkInterfaces",
		"ec2:UnassignPrivateIpAddresses",
		"events:PutEvents",
		"lambda:GetFunctionConfiguration",
		"lambda:InvokeAsync",
		"lambda:InvokeFunction",
		"lambda:InvokeFunctionUrl",
		"logs:CreateLogGroup",
		"logs:CreateLogStream",
		"logs:DescribeLogGroups",
		"logs:DescribeLogStreams",
		"logs:PutLogEvents",
		"rds-data:BatchExecuteStatement",
		"rds-data:BeginTransaction",
		"rds-data:CommitTransaction",
		"rds-data:ExecuteStatement",
		"rds-data:RollbackTransaction",
		"rds-db:connect",
		"s3:AbortMultipartUpload",
		"s3:DeleteObject",
		"s3:DeleteObjectTagging",
		"s3:GetBucketLocation",
		"s3:GetObject",
		"s3:GetObjectAttributes",
		"s3:GetObjectTagging",
		"s3:GetObjectVersion",
		"s3:ListBucket",
		"s3:ListBucketMultipartUploads",
		"s3:ListMultipartUploadParts",
		"s3:PutObject",
		"s3:PutObjectTagging",
		"ses:SendEmail",
		"ses:SendRawEmail",
		"sns:Publish",
		"sqs:ChangeMessageVisibility",
		"sqs:DeleteMessage",
		"sqs:GetQueueAttributes",
		"sqs:GetQueueUrl",
		"sqs:ReceiveMessage",
		"sqs:SendMessage",
		"ssmmessages:CreateControlChannel",
		"ssmmessages:CreateDataChannel",
		"ssmmessages:OpenControlChannel",
		"ssmmessages:OpenDataChannel",
		"states:DescribeExecution",
		"states:StartExecution",
		"states:StartSyncExecution",
		"xray:PutTelemetryRecords",
		"xray:PutTraceSegments",
	}
}

func appBoundaryKeyActions() []string {
	return []string{
		"kms:Decrypt",
		"kms:DescribeKey",
		"kms:Encrypt",
		"kms:GenerateDataKey",
		"kms:GenerateDataKeyWithoutPlaintext",
		"kms:ReEncryptFrom",
		"kms:ReEncryptTo",
	}
}

func appBoundaryKeyStatement(ns Namespace, tier environment.Tier, broughtKey string) iam.Statement {
	if broughtKey != "" {
		return iam.Statement{Actions: appBoundaryKeyActions(), Resources: []string{broughtKey}}
	}
	return iam.Statement{
		Actions:   appBoundaryKeyActions(),
		Resources: []string{iam.AnyResource},
		Condition: map[string]any{
			"ForAnyValue:StringEquals": map[string]any{"kms:ResourceAliases": ns.variablesKeyAliasFor(tier)},
		},
	}
}

func appBoundaryResource(ns Namespace, tier environment.Tier, broughtKey string) cfn.Resource {
	return cfn.Resource{
		ID:   "AppBoundary",
		Type: "AWS::IAM::ManagedPolicy",
		Metadata: cfn.Metadata{
			Description: "The ceiling every app role of this tier is made under: a deploy may only mint roles bounded by it, so the widest such role reaches these actions, this tier's variable key, and no IAM, STS, parameter or secret call.",
		},
		Properties: map[string]any{
			"ManagedPolicyName": ns.AppBoundaryNameFor(tier),
			"Description":       "Permissions boundary for the roles Ocel creates for apps in the " + string(tier) + " class.",
			"PolicyDocument": iam.Document([]iam.Statement{
				{Actions: appBoundaryActions(), Resources: []string{iam.AnyResource}},
				appBoundaryKeyStatement(ns, tier, broughtKey),
				{
					Actions:   []string{"ecs:DescribeServices", "ecs:UpdateService"},
					Resources: []string{appWorkerServiceARN(ns, tier)},
					Condition: map[string]any{
						"StringEquals": map[string]any{"aws:ResourceTag/" + iam.ManagedByTagKey: string(ns)},
					},
				},
				{Actions: []string{"ecr:GetAuthorizationToken"}, Resources: []string{iam.AnyResource}},
				{
					Actions:   []string{"ecr:BatchCheckLayerAvailability", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"},
					Resources: []string{registry.RepositoriesARN(string(ns))},
				},
			}),
		},
	}
}

func appWorkerServiceARN(ns Namespace, tier environment.Tier) string {
	return "arn:aws:ecs:*:*:service/" + awsports.ContainerClusterName(string(ns), tier) + "/" + naming.AppFence(string(ns)) + "*"
}

func appBoundaryOutput() cfn.Output {
	return cfn.Output{
		Name:        outputAppBoundaryARN,
		Description: "Permissions boundary every role a deploy creates for an app must name, and the only one the deploy credentials may name.",
		Value:       cfn.Ref("AppBoundary"),
	}
}
