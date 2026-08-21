package bootstrap

import (
	"encoding/json"
	"fmt"
	"slices"
)

const (
	callerAccount = "${aws:PrincipalAccount}"

	managedByTagKey     = "ocel:managed-by"
	managedByTagPattern = "ocel-cli/*"

	lambdaServicePrincipal = "lambda.amazonaws.com"

	lambdaBasicExecutionPolicyARN = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
	lambdaVPCAccessPolicyARN      = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
)

const (
	substrateBucketARN = "arn:aws:s3:::" + StackName + "*"
	substrateObjectARN = substrateBucketARN + "/*"

	substrateTableARN     = "arn:aws:dynamodb:*:*:table/" + StackName + "*"
	substrateTablePartARN = substrateTableARN + "/*"

	substrateStackARN     = "arn:aws:cloudformation:*:*:stack/" + StackName + "*/*"
	substrateChangeSetARN = "arn:aws:cloudformation:*:*:changeSet/ocel-*/*"
	substrateRoleARN      = "arn:aws:iam::*:role/" + StackName + "*"
	substrateFunctionARN  = "arn:aws:lambda:*:*:function:" + StackName + "*"
	substrateQueueARN     = "arn:aws:sqs:*:*:ocel-*"

	edgeUserARN = "arn:aws:iam::*:user/" + EdgeUserName + "*"

	appBoundaryARN = "arn:aws:iam::*:policy/" + AppBoundaryName + "*"

	anyKeyARN    = "arn:aws:kms:*:*:key/*"
	varsAliasARN = "arn:aws:kms:*:*:alias/ocel-vars-*"

	parameterARNPrefix     = "arn:aws:ssm:*:*:parameter"
	passphraseParameterARN = parameterARNPrefix + PassphraseParamName
	edgeParameterARN       = parameterARNPrefix + "/ocel/edge/*"
	originParameterARN     = parameterARNPrefix + "/ocel/origin/*"
	stackRecordTreeARN     = parameterARNPrefix + "/ocel/rootstack*"
	stackRecordARN         = stackRecordTreeARN + "/*"
	ocelParameterARN       = parameterARNPrefix + "/ocel/*"

	appBucketARN        = "arn:aws:s3:::*"
	appFunctionARN      = "arn:aws:lambda:*:*:function:*"
	appLayerARN         = "arn:aws:lambda:*:*:layer:*"
	appRoleARN          = "arn:aws:iam::*:role/*"
	appSecretARN        = "arn:aws:secretsmanager:*:*:secret:rds!cluster-*"
	appClusterARN       = "arn:aws:rds:*:*:cluster:*"
	appInstanceARN      = "arn:aws:rds:*:*:db:*"
	appSubnetGroupARN   = "arn:aws:rds:*:*:subgrp:*"
	appSecurityGroupARN = "arn:aws:ec2:*:*:security-group/*"
	appVPCARN           = "arn:aws:ec2:*:*:vpc/*"

	substrateEventSourceARN = "arn:aws:lambda:*:*:event-source-mapping:*"

	unscopedResource = "*"
)

type grantStatement struct {
	actions   []string
	resources []string
	condition map[string]any
}

func inCallerAccount() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceAccount": callerAccount}}
}

func taggedOnCreate() map[string]any {
	return map[string]any{"StringLike": map[string]any{"aws:RequestTag/" + managedByTagKey: managedByTagPattern}}
}

func taggedByOcel() map[string]any {
	return map[string]any{"StringLike": map[string]any{"aws:ResourceTag/" + managedByTagKey: managedByTagPattern}}
}

func withinAppBoundary() map[string]any {
	return map[string]any{"StringEquals": map[string]any{
		"iam:PermissionsBoundary": []string{appBoundaryARNFor(ClassProduction), appBoundaryARNFor(ClassPreview)},
	}}
}

func mergeConditions(conditions ...map[string]any) map[string]any {
	merged := map[string]any{}
	for _, condition := range conditions {
		for operator, operands := range condition {
			standing, ok := merged[operator].(map[string]any)
			if !ok {
				merged[operator] = operands
				continue
			}
			for key, value := range operands.(map[string]any) {
				standing[key] = value
			}
		}
	}
	return merged
}

func attachedPolicyIsAWSLambdaExecution(resourceTagged bool) map[string]any {
	condition := map[string]any{
		"ArnEquals": map[string]any{"iam:PolicyARN": []string{lambdaBasicExecutionPolicyARN, lambdaVPCAccessPolicyARN}},
	}
	if resourceTagged {
		condition["StringLike"] = map[string]any{"aws:ResourceTag/" + managedByTagKey: managedByTagPattern}
	}
	return condition
}

func passedToLambda(resourceTagged bool) map[string]any {
	condition := map[string]any{
		"StringEquals": map[string]any{"iam:PassedToService": lambdaServicePrincipal},
	}
	if resourceTagged {
		condition["StringLike"] = map[string]any{"aws:ResourceTag/" + managedByTagKey: managedByTagPattern}
	}
	return condition
}

func varsKeyLifecycleActions() []string {
	return []string{
		"kms:CancelKeyDeletion",
		"kms:DescribeKey",
		"kms:DisableKey",
		"kms:DisableKeyRotation",
		"kms:EnableKey",
		"kms:EnableKeyRotation",
		"kms:GetKeyPolicy",
		"kms:GetKeyRotationStatus",
		"kms:ListResourceTags",
		"kms:PutKeyPolicy",
		"kms:ScheduleKeyDeletion",
		"kms:TagResource",
		"kms:UntagResource",
	}
}

func substrateAccess() []grantStatement {
	return []grantStatement{
		{
			actions: []string{
				"s3:AbortMultipartUpload",
				"s3:DeleteObject",
				"s3:GetObject",
				"s3:ListMultipartUploadParts",
				"s3:PutObject",
			},
			resources: []string{substrateObjectARN},
			condition: inCallerAccount(),
		},
		{
			actions:   []string{"s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"},
			resources: []string{substrateBucketARN},
			condition: inCallerAccount(),
		},
		{
			actions: []string{
				"dynamodb:BatchGetItem",
				"dynamodb:BatchWriteItem",
				"dynamodb:DeleteItem",
				"dynamodb:DescribeTable",
				"dynamodb:GetItem",
				"dynamodb:PutItem",
				"dynamodb:Query",
				"dynamodb:UpdateItem",
			},
			resources: []string{substrateTableARN, substrateTablePartARN},
		},
		{
			actions:   []string{"ssm:GetParameter", "ssm:GetParameters"},
			resources: []string{passphraseParameterARN, edgeParameterARN, originParameterARN, stackRecordARN},
		},
		{
			actions:   []string{"ssm:DeleteParameter", "ssm:PutParameter"},
			resources: []string{stackRecordARN},
		},
		{
			actions:   []string{"kms:Decrypt", "kms:DescribeKey", "kms:Encrypt", "kms:GenerateDataKey"},
			resources: []string{anyKeyARN},
			condition: map[string]any{
				"ForAnyValue:StringLike": map[string]any{"kms:ResourceAliases": varsKeyAliasFor("*")},
			},
		},
		{
			actions:   []string{"cloudformation:DescribeStacks"},
			resources: []string{substrateStackARN},
		},
		{
			actions:   []string{"sts:GetCallerIdentity"},
			resources: []string{unscopedResource},
		},
	}
}

func appProvisioning() []grantStatement {
	return []grantStatement{
		{
			actions:   []string{"lambda:CreateFunction"},
			resources: []string{appFunctionARN},
			condition: taggedOnCreate(),
		},
		{
			actions: []string{
				"lambda:AddPermission",
				"lambda:CreateFunctionUrlConfig",
				"lambda:DeleteFunction",
				"lambda:DeleteFunctionUrlConfig",
				"lambda:GetFunction",
				"lambda:GetFunctionConfiguration",
				"lambda:GetFunctionUrlConfig",
				"lambda:GetPolicy",
				"lambda:InvokeFunction",
				"lambda:ListTags",
				"lambda:PublishVersion",
				"lambda:RemovePermission",
				"lambda:TagResource",
				"lambda:UntagResource",
				"lambda:UpdateFunctionCode",
				"lambda:UpdateFunctionConfiguration",
				"lambda:UpdateFunctionUrlConfig",
			},
			resources: []string{appFunctionARN},
			condition: taggedByOcel(),
		},
		{
			actions: []string{
				"lambda:DeleteLayerVersion",
				"lambda:GetLayerVersion",
				"lambda:ListLayerVersions",
				"lambda:PublishLayerVersion",
			},
			resources: []string{appLayerARN},
		},
		{
			actions:   []string{"iam:CreateRole"},
			resources: []string{appRoleARN},
			condition: mergeConditions(taggedOnCreate(), withinAppBoundary()),
		},
		{
			actions: []string{
				"iam:DeleteRole",
				"iam:GetRole",
				"iam:GetRolePolicy",
				"iam:ListAttachedRolePolicies",
				"iam:ListInstanceProfilesForRole",
				"iam:ListRolePolicies",
				"iam:ListRoleTags",
				"iam:TagRole",
				"iam:UntagRole",
				"iam:UpdateRole",
			},
			resources: []string{appRoleARN},
			condition: taggedByOcel(),
		},
		{
			actions: []string{
				"iam:DeleteRolePolicy",
				"iam:PutRolePermissionsBoundary",
				"iam:PutRolePolicy",
			},
			resources: []string{appRoleARN},
			condition: mergeConditions(taggedByOcel(), withinAppBoundary()),
		},
		{
			actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			resources: []string{appRoleARN},
			condition: mergeConditions(attachedPolicyIsAWSLambdaExecution(true), withinAppBoundary()),
		},
		{
			actions:   []string{"iam:PassRole"},
			resources: []string{appRoleARN},
			condition: passedToLambda(true),
		},
		{
			actions:   []string{"s3:CreateBucket"},
			resources: []string{appBucketARN},
			condition: inCallerAccount(),
		},
		{
			actions: []string{
				"s3:DeleteBucket",
				"s3:GetAccelerateConfiguration",
				"s3:GetBucket*",
				"s3:GetEncryptionConfiguration",
				"s3:GetLifecycleConfiguration",
				"s3:GetReplicationConfiguration",
				"s3:PutBucketCORS",
				"s3:PutBucketNotification",
				"s3:PutBucketPublicAccessBlock",
				"s3:PutBucketTagging",
			},
			resources: []string{appBucketARN},
			condition: inCallerAccount(),
		},
		{
			actions: []string{
				"rds:AddTagsToResource",
				"rds:CreateDBCluster",
				"rds:CreateDBInstance",
				"rds:CreateDBSubnetGroup",
			},
			resources: []string{appClusterARN, appInstanceARN, appSubnetGroupARN},
			condition: taggedOnCreate(),
		},
		{
			actions: []string{
				"rds:DeleteDBCluster",
				"rds:DeleteDBInstance",
				"rds:DeleteDBSubnetGroup",
				"rds:DescribeDBClusters",
				"rds:DescribeDBInstances",
				"rds:DescribeDBSubnetGroups",
				"rds:ListTagsForResource",
				"rds:ModifyDBCluster",
				"rds:ModifyDBInstance",
				"rds:ModifyDBSubnetGroup",
				"rds:RemoveTagsFromResource",
			},
			resources: []string{appClusterARN, appInstanceARN, appSubnetGroupARN},
			condition: taggedByOcel(),
		},
		{
			actions: []string{
				"ec2:DescribeNetworkInterfaces",
				"ec2:DescribeSecurityGroupRules",
				"ec2:DescribeSecurityGroups",
				"ec2:DescribeSubnets",
				"ec2:DescribeVpcs",
				"rds:DescribeDBEngineVersions",
				"rds:DescribeOrderableDBInstanceOptions",
			},
			resources: []string{unscopedResource},
		},
		{
			actions:   []string{"ec2:CreateSecurityGroup"},
			resources: []string{appSecurityGroupARN, appVPCARN},
			condition: taggedOnCreate(),
		},
		{
			actions:   []string{"ec2:CreateTags"},
			resources: []string{appSecurityGroupARN},
			condition: map[string]any{
				"StringEquals": map[string]any{"ec2:CreateAction": "CreateSecurityGroup"},
			},
		},
		{
			actions: []string{
				"ec2:AuthorizeSecurityGroupEgress",
				"ec2:AuthorizeSecurityGroupIngress",
				"ec2:DeleteSecurityGroup",
				"ec2:ModifySecurityGroupRules",
				"ec2:RevokeSecurityGroupEgress",
				"ec2:RevokeSecurityGroupIngress",
			},
			resources: []string{appSecurityGroupARN},
			condition: taggedByOcel(),
		},
		{
			actions:   []string{"secretsmanager:DescribeSecret", "secretsmanager:GetSecretValue"},
			resources: []string{appSecretARN},
		},
	}
}

func substrateProvisioning() []grantStatement {
	return []grantStatement{
		{
			actions: []string{
				"cloudformation:CreateChangeSet",
				"cloudformation:CreateStack",
				"cloudformation:DeleteStack",
				"cloudformation:DescribeStackEvents",
			},
			resources: []string{substrateStackARN},
		},
		{
			actions: []string{
				"cloudformation:DeleteChangeSet",
				"cloudformation:DescribeChangeSet",
				"cloudformation:ExecuteChangeSet",
			},
			resources: []string{substrateStackARN, substrateChangeSetARN},
		},
		{
			actions:   []string{"s3:CreateBucket"},
			resources: []string{substrateBucketARN},
		},
		{
			actions: []string{
				"s3:DeleteBucket",
				"s3:DeleteBucketPolicy",
				"s3:GetBucket*",
				"s3:GetEncryptionConfiguration",
				"s3:GetLifecycleConfiguration",
				"s3:ListBucketVersions",
				"s3:PutBucketPolicy",
				"s3:PutBucketPublicAccessBlock",
				"s3:PutBucketTagging",
				"s3:PutBucketVersioning",
				"s3:PutEncryptionConfiguration",
				"s3:PutLifecycleConfiguration",
			},
			resources: []string{substrateBucketARN},
			condition: inCallerAccount(),
		},
		{
			actions:   []string{"s3:DeleteObjectVersion", "s3:GetObjectVersion"},
			resources: []string{substrateObjectARN},
			condition: inCallerAccount(),
		},
		{
			actions: []string{
				"dynamodb:CreateTable",
				"dynamodb:DeleteTable",
				"dynamodb:DescribeContinuousBackups",
				"dynamodb:DescribeStream",
				"dynamodb:DescribeTimeToLive",
				"dynamodb:ListStreams",
				"dynamodb:ListTagsOfResource",
				"dynamodb:TagResource",
				"dynamodb:UntagResource",
				"dynamodb:UpdateContinuousBackups",
				"dynamodb:UpdateTable",
				"dynamodb:UpdateTimeToLive",
			},
			resources: []string{substrateTableARN, substrateTablePartARN},
		},
		{
			actions:   []string{"kms:CreateKey", "kms:TagResource"},
			resources: []string{unscopedResource},
			condition: map[string]any{
				"StringEquals": map[string]any{"aws:RequestTag/" + varsKeyComponentTagKey: varsKeyComponentTagValue},
			},
		},
		{
			actions:   varsKeyLifecycleActions(),
			resources: []string{anyKeyARN},
			condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + varsKeyComponentTagKey: varsKeyComponentTagValue},
			},
		},
		{
			actions:   []string{"kms:DescribeKey", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus", "kms:ListResourceTags"},
			resources: []string{anyKeyARN},
			condition: map[string]any{
				"ForAnyValue:StringLike": map[string]any{"kms:ResourceAliases": varsKeyAliasFor("*")},
			},
		},
		{
			actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			resources: []string{varsAliasARN},
		},
		{
			actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			resources: []string{anyKeyARN},
			condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + varsKeyComponentTagKey: varsKeyComponentTagValue},
			},
		},
		{
			actions: []string{
				"iam:CreatePolicy",
				"iam:CreatePolicyVersion",
				"iam:DeletePolicy",
				"iam:DeletePolicyVersion",
				"iam:GetPolicy",
				"iam:GetPolicyVersion",
				"iam:ListEntitiesForPolicy",
				"iam:ListPolicyTags",
				"iam:ListPolicyVersions",
				"iam:TagPolicy",
				"iam:UntagPolicy",
			},
			resources: []string{appBoundaryARN},
		},
		{
			actions:   []string{"iam:CreateRole", "iam:TagRole"},
			resources: []string{substrateRoleARN},
		},
		{
			actions: []string{
				"iam:DeleteRole",
				"iam:DeleteRolePolicy",
				"iam:GetRole",
				"iam:GetRolePolicy",
				"iam:ListAttachedRolePolicies",
				"iam:ListRolePolicies",
				"iam:ListRoleTags",
				"iam:PutRolePolicy",
				"iam:UntagRole",
				"iam:UpdateAssumeRolePolicy",
				"iam:UpdateRole",
			},
			resources: []string{substrateRoleARN},
		},
		{
			actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			resources: []string{substrateRoleARN},
			condition: attachedPolicyIsAWSLambdaExecution(false),
		},
		{
			actions:   []string{"iam:PassRole"},
			resources: []string{substrateRoleARN},
			condition: passedToLambda(false),
		},
		{
			actions: []string{
				"lambda:AddPermission",
				"lambda:CreateFunction",
				"lambda:CreateFunctionUrlConfig",
				"lambda:DeleteFunction",
				"lambda:DeleteFunctionUrlConfig",
				"lambda:GetFunction",
				"lambda:GetFunctionConfiguration",
				"lambda:GetFunctionUrlConfig",
				"lambda:GetPolicy",
				"lambda:ListTags",
				"lambda:RemovePermission",
				"lambda:TagResource",
				"lambda:UntagResource",
				"lambda:UpdateFunctionCode",
				"lambda:UpdateFunctionConfiguration",
				"lambda:UpdateFunctionUrlConfig",
			},
			resources: []string{substrateFunctionARN},
		},
		{
			actions:   []string{"lambda:CreateEventSourceMapping"},
			resources: []string{unscopedResource},
			condition: map[string]any{
				"ArnLike": map[string]any{"lambda:FunctionArn": substrateFunctionARN},
			},
		},
		{
			actions: []string{
				"lambda:DeleteEventSourceMapping",
				"lambda:GetEventSourceMapping",
				"lambda:UpdateEventSourceMapping",
			},
			resources: []string{substrateEventSourceARN},
			condition: inCallerAccount(),
		},
		{
			actions: []string{
				"sqs:CreateQueue",
				"sqs:DeleteQueue",
				"sqs:GetQueueAttributes",
				"sqs:GetQueueUrl",
				"sqs:ListQueueTags",
				"sqs:SetQueueAttributes",
				"sqs:TagQueue",
				"sqs:UntagQueue",
			},
			resources: []string{substrateQueueARN},
		},
		{
			actions:   []string{"ssm:AddTagsToResource", "ssm:DeleteParameter", "ssm:DeleteParameters", "ssm:PutParameter"},
			resources: []string{ocelParameterARN},
		},
		{
			actions:   []string{"ssm:GetParametersByPath"},
			resources: []string{stackRecordTreeARN, stackRecordARN},
		},
	}
}

func edgePrincipal() []grantStatement {
	return []grantStatement{
		{
			actions: []string{
				"iam:CreateAccessKey",
				"iam:CreateUser",
				"iam:DeleteAccessKey",
				"iam:DeleteUser",
				"iam:DeleteUserPolicy",
				"iam:GetUser",
				"iam:GetUserPolicy",
				"iam:ListAccessKeys",
				"iam:ListAttachedUserPolicies",
				"iam:ListGroupsForUser",
				"iam:ListUserPolicies",
				"iam:ListUserTags",
				"iam:PutUserPolicy",
				"iam:UpdateAccessKey",
			},
			resources: []string{edgeUserARN},
		},
	}
}

func deployTier() []grantStatement {
	return slices.Concat(substrateAccess(), appProvisioning())
}

func bootstrapTier() []grantStatement {
	return slices.Concat(substrateAccess(), appProvisioning(), substrateProvisioning(), edgePrincipal())
}

func DeployCredentialPolicy() (string, error) {
	return credentialPolicy("deploy", deployTier())
}

func BootstrapCredentialPolicy() (string, error) {
	return credentialPolicy("bootstrap", bootstrapTier())
}

func credentialPolicy(tier string, grants []grantStatement) (string, error) {
	statements := make([]map[string]any, 0, len(grants))
	for _, grant := range grants {
		statement := map[string]any{
			"Effect":   "Allow",
			"Action":   oneOrMany(grant.actions),
			"Resource": oneOrMany(grant.resources),
		}
		if len(grant.condition) > 0 {
			statement["Condition"] = grant.condition
		}
		statements = append(statements, statement)
	}
	out, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render the %s credential policy: %w", tier, err)
	}
	return string(out), nil
}

func oneOrMany(values []string) any {
	if len(values) == 1 {
		return values[0]
	}
	return values
}
