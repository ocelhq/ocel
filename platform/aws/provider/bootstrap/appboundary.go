package bootstrap

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

const outputAppBoundaryARN = "AppBoundaryArn"

func appBoundaryARNFor(ns Namespace, tier environment.Tier) string {
	return "arn:aws:iam::" + "${aws:PrincipalAccount}" + ":policy/" + ns.AppBoundaryNameFor(tier)
}

func appBoundaryActions() []string {
	return []string{
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

func appBoundarySecretActions() []string {
	return []string{
		"secretsmanager:DescribeSecret",
		"secretsmanager:GetSecretValue",
	}
}

func yamlActions(actions []string) string {
	var out strings.Builder
	for _, action := range actions {
		fmt.Fprintf(&out, "              - %s\n", action)
	}
	return out.String()
}

func appBoundaryKeyStatement(ns Namespace, tier environment.Tier, broughtKey string) string {
	if broughtKey != "" {
		return fmt.Sprintf(`          - Effect: Allow
            Action:
%s            Resource: %q
`, yamlActions(appBoundaryKeyActions()), broughtKey)
	}
	return fmt.Sprintf(`          - Effect: Allow
            Action:
%s            Resource: '*'
            Condition:
              ForAnyValue:StringEquals:
                kms:ResourceAliases: %s
`, yamlActions(appBoundaryKeyActions()), ns.varsKeyAliasFor(tier))
}

func appBoundaryResource(ns Namespace, tier environment.Tier, broughtKey string) string {
	return fmt.Sprintf(`  AppBoundary:
    Type: AWS::IAM::ManagedPolicy
    Metadata:
      Description: "The ceiling every app role of this tier is made under: a deploy may only mint roles bounded by it, so the widest such role reaches these actions, this tier's variable key, the master secrets of clusters deploys create, and no IAM, STS or parameter call."
    Properties:
      ManagedPolicyName: %s
      Description: "Permissions boundary for the roles Ocel creates for apps in the %s class."
      PolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Action:
%s            Resource: '*'
%s          - Effect: Allow
            Action:
%s            Resource: '%s'
            Condition:
              StringLike:
                'aws:ResourceTag/%s': '%s'
          - Effect: Allow
            Action:
              - ecr:GetAuthorizationToken
            Resource: '*'
          - Effect: Allow
            Action:
              - ecr:BatchCheckLayerAvailability
              - ecr:BatchGetImage
              - ecr:GetDownloadUrlForLayer
            Resource: 'arn:aws:ecr:*:*:repository/%s/*'
`, ns.AppBoundaryNameFor(tier), tier,
		yamlActions(appBoundaryActions()),
		appBoundaryKeyStatement(ns, tier, broughtKey),
		yamlActions(appBoundarySecretActions()), appSecretARN, managedSecretClusterTagKey, appClusterARN,
		registry.Namespace)
}

func appBoundaryOutput() string {
	return fmt.Sprintf(`  %s:
    Description: "Permissions boundary every role a deploy creates for an app must name, and the only one the deploy credentials may name."
    Value: !Ref AppBoundary
`, outputAppBoundaryARN)
}
