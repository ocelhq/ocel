package bootstrap

import (
	"fmt"
	"strings"
)

const (
	AppBoundaryName        = "ocel-app-boundary"
	AppBoundaryPreviewName = AppBoundaryName + "-preview"

	outputAppBoundaryARN = "AppBoundaryArn"
)

func AppBoundaryNameFor(class string) string {
	if class == ClassPreview {
		return AppBoundaryPreviewName
	}
	return AppBoundaryName
}

func appBoundaryARNFor(class string) string {
	return "arn:aws:iam::" + "${aws:PrincipalAccount}" + ":policy/" + AppBoundaryNameFor(class)
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
		"kms:Decrypt",
		"kms:DescribeKey",
		"kms:Encrypt",
		"kms:GenerateDataKey",
		"kms:GenerateDataKeyWithoutPlaintext",
		"kms:ReEncryptFrom",
		"kms:ReEncryptTo",
		"lambda:GetFunctionConfiguration",
		"lambda:InvokeAsync",
		"lambda:InvokeFunction",
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
		"secretsmanager:DescribeSecret",
		"secretsmanager:GetSecretValue",
		"ses:SendEmail",
		"ses:SendRawEmail",
		"sns:Publish",
		"sqs:ChangeMessageVisibility",
		"sqs:DeleteMessage",
		"sqs:GetQueueAttributes",
		"sqs:GetQueueUrl",
		"sqs:ReceiveMessage",
		"sqs:SendMessage",
		"ssm:GetParameter",
		"ssm:GetParameters",
		"ssm:GetParametersByPath",
		"states:DescribeExecution",
		"states:StartExecution",
		"states:StartSyncExecution",
		"xray:PutTelemetryRecords",
		"xray:PutTraceSegments",
	}
}

func appBoundaryResource(class string) string {
	var actions strings.Builder
	for _, action := range appBoundaryActions() {
		fmt.Fprintf(&actions, "              - %s\n", action)
	}
	return fmt.Sprintf(`  AppBoundary:
    Type: AWS::IAM::ManagedPolicy
    Metadata:
      Description: "The ceiling every role Ocel creates for an app in this class is made under: a deploy may only mint and write policies onto roles carrying it, so the widest such role reaches these actions and no IAM, STS or account-level call."
    Properties:
      ManagedPolicyName: %s
      Description: "Permissions boundary for the roles Ocel creates for apps in the %s class."
      PolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Action:
%s            Resource: '*'
`, AppBoundaryNameFor(class), class, actions.String())
}

func appBoundaryOutput() string {
	return fmt.Sprintf(`  %s:
    Description: "Permissions boundary every role a deploy creates for an app must carry, and the only one the deploy credentials may name."
    Value: !Ref AppBoundary
`, outputAppBoundaryARN)
}
