package bootstrap

import (
	"encoding/json"
	"fmt"
	"slices"

	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

const (
	callerAccount = "${aws:PrincipalAccount}"

	managedByTagKey     = "ocel:managed-by"
	managedByTagPattern = "ocel-cli/*"

	LambdaServicePrincipal = "lambda.amazonaws.com"

	ecsTasksPrincipal = "ecs-tasks.amazonaws.com"

	LambdaBasicExecutionPolicyARN = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
	lambdaVPCAccessPolicyARN      = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
	ecsTaskExecutionPolicyARN     = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
)

const (
	AnyKeyARN = "arn:aws:kms:*:*:key/*"

	parameterARNPrefix = "arn:aws:ssm:*:*:parameter"

	appScopePrefix = awsports.AppScope + "-"

	appBucketARN      = "arn:aws:s3:::" + appScopePrefix + "*"
	appFunctionARN    = "arn:aws:lambda:*:*:function:*"
	appRoleARN        = "arn:aws:iam::*:role/*"
	appSecretARN      = "arn:aws:secretsmanager:*:*:secret:rds!cluster-*"
	appClusterARN     = "arn:aws:rds:*:*:cluster:" + appScopePrefix + "*"
	appInstanceARN    = "arn:aws:rds:*:*:db:" + appScopePrefix + "*"
	appSubnetGroupARN = "arn:aws:rds:*:*:subgrp:" + appScopePrefix + "*"

	appSecurityGroupARN  = "arn:aws:ec2:*:*:security-group/*"
	appVPCARN            = "arn:aws:ec2:*:*:vpc/*"
	appRepositoryARN     = "arn:aws:ecr:*:*:repository/" + registry.Namespace + "/*"
	appLogGroupARN       = "arn:aws:logs:*:*:log-group:/ocel/*"
	functionLogGroupARN  = "arn:aws:logs:*:*:log-group:/aws/lambda/*"
	appTargetGroupARN    = "arn:aws:elasticloadbalancing:*:*:targetgroup/*/*"
	appTaskDefinitionARN = "arn:aws:ecs:*:*:task-definition/*:*"

	managedSecretClusterTagKey = "aws:rds:primaryDBClusterArn"

	containerClusterARN    = "arn:aws:ecs:*:*:cluster/ocel-*"
	containerServiceARN    = "arn:aws:ecs:*:*:service/ocel-*/*"
	containerBalancerARN   = "arn:aws:elasticloadbalancing:*:*:loadbalancer/app/ocel-*/*"
	containerListenerARN   = "arn:aws:elasticloadbalancing:*:*:listener/app/ocel-*/*/*"
	containerRuleARN       = "arn:aws:elasticloadbalancing:*:*:listener-rule/app/ocel-*/*/*/*"
	containerVPCOriginARN  = "arn:aws:cloudfront::*:vpcorigin/*"
	vpcOriginLinkedRoleARN = "arn:aws:iam::*:role/aws-service-role/vpcorigin.cloudfront.amazonaws.com/*"
	ecsLinkedRoleARN       = "arn:aws:iam::*:role/aws-service-role/ecs.amazonaws.com/*"
	elbLinkedRoleARN       = "arn:aws:iam::*:role/aws-service-role/elasticloadbalancing.amazonaws.com/*"

	bootstrapEventSourceARN = "arn:aws:lambda:*:*:event-source-mapping:*"

	UnscopedResource = "*"
)

type ScopedARNs struct {
	bootstrapBucket     string
	bootstrapObject     string
	BootstrapTable      string
	BootstrapTablePart  string
	BootstrapStack      string
	bootstrapChangeSet  string
	runtimeStack        string
	runtimeChangeSet    string
	runtimeLayer        string
	runtimeLayerVersion string
	bootstrapRole       string
	bootstrapFunction   string
	bootstrapLogGroup   string
	bootstrapQueue      string
	edgeUser            string
	appBoundary         string
	varsAlias           string
	passphraseParam     string
	edgeParam           string
	originParam         string
	stackRecordTree     string
	stackRecord         string
	anyParam            string
}

func (n Namespace) ScopedARNs() ScopedARNs {
	core := n.CoreStackName()
	a := ScopedARNs{
		bootstrapBucket:    "arn:aws:s3:::" + core + "*",
		BootstrapTable:     "arn:aws:dynamodb:*:*:table/" + core + "*",
		BootstrapStack:     "arn:aws:cloudformation:*:*:stack/" + core + "*/*",
		bootstrapChangeSet: "arn:aws:cloudformation:*:*:changeSet/" + string(n) + "-*/*",
		runtimeStack:       "arn:aws:cloudformation:*:*:stack/" + core + "-runtime*/*",
		runtimeChangeSet:   "arn:aws:cloudformation:*:*:changeSet/" + core + "-runtime*/*",
		runtimeLayer:       "arn:aws:lambda:*:*:layer:" + string(n) + "-runtime*",
		bootstrapRole:      "arn:aws:iam::*:role/" + core + "*",
		bootstrapFunction:  "arn:aws:lambda:*:*:function:" + core + "*",
		bootstrapLogGroup:  "arn:aws:logs:*:*:log-group:/aws/lambda/" + core + "*",
		bootstrapQueue:     "arn:aws:sqs:*:*:" + string(n) + "-*",
		edgeUser:           "arn:aws:iam::*:user/" + string(n) + "-edge*",
		appBoundary:        "arn:aws:iam::*:policy/" + n.AppBoundaryNameFor(ClassProduction) + "*",
		varsAlias:          "arn:aws:kms:*:*:alias/" + string(n) + "-vars-*",
		passphraseParam:    parameterARNPrefix + n.PassphraseParamName(),
		edgeParam:          parameterARNPrefix + n.paramRoot() + "/edge/*",
		originParam:        parameterARNPrefix + n.paramRoot() + "/origin/*",
		stackRecordTree:    parameterARNPrefix + n.stackRecordRoot() + "*",
		anyParam:           parameterARNPrefix + n.paramRoot() + "/*",
	}
	a.bootstrapObject = a.bootstrapBucket + "/*"
	a.runtimeLayerVersion = a.runtimeLayer + ":*"
	a.BootstrapTablePart = a.BootstrapTable + "/*"
	a.stackRecord = a.stackRecordTree + "/*"
	return a
}

type GrantStatement struct {
	Actions   []string
	Resources []string
	Condition map[string]any
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

func managedByAnAppCluster() map[string]any {
	return map[string]any{"StringLike": map[string]any{"aws:ResourceTag/" + managedSecretClusterTagKey: appClusterARN}}
}

func withinAppBoundary(ns Namespace) map[string]any {
	return map[string]any{"StringEquals": map[string]any{
		"iam:PermissionsBoundary": []string{appBoundaryARNFor(ns, ClassProduction), appBoundaryARNFor(ns, ClassPreview)},
	}}
}

func mergeConditions(conditions ...map[string]any) map[string]any {
	merged := map[string]any{}
	for _, condition := range conditions {
		for operator, operands := range condition {
			existing, ok := merged[operator].(map[string]any)
			if !ok {
				merged[operator] = operands
				continue
			}
			for key, value := range operands.(map[string]any) {
				existing[key] = value
			}
		}
	}
	return merged
}

func attachedPolicyIsAServiceRole(resourceTagged bool) map[string]any {
	condition := map[string]any{
		"ArnEquals": map[string]any{"iam:PolicyARN": []string{LambdaBasicExecutionPolicyARN, lambdaVPCAccessPolicyARN, ecsTaskExecutionPolicyARN}},
	}
	if resourceTagged {
		condition["StringLike"] = map[string]any{"aws:ResourceTag/" + managedByTagKey: managedByTagPattern}
	}
	return condition
}

func passedToLambda(resourceTagged bool) map[string]any {
	return passedTo(LambdaServicePrincipal, resourceTagged)
}

func passedToECSTasks() map[string]any {
	return passedTo(ecsTasksPrincipal, true)
}

func linkedRoleFor(service string) map[string]any {
	return map[string]any{"StringEquals": map[string]any{"iam:AWSServiceName": service}}
}

func passedTo(service any, resourceTagged bool) map[string]any {
	condition := map[string]any{
		"StringEquals": map[string]any{"iam:PassedToService": service},
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

func bootstrapAccess(r ScopedARNs) []GrantStatement {
	return []GrantStatement{
		{
			Actions: []string{
				"s3:AbortMultipartUpload",
				"s3:DeleteObject",
				"s3:GetObject",
				"s3:ListMultipartUploadParts",
				"s3:PutObject",
			},
			Resources: []string{r.bootstrapObject},
			Condition: inCallerAccount(),
		},
		{
			Actions:   []string{"s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"},
			Resources: []string{r.bootstrapBucket},
			Condition: inCallerAccount(),
		},
		{
			Actions: []string{
				"dynamodb:BatchGetItem",
				"dynamodb:BatchWriteItem",
				"dynamodb:DeleteItem",
				"dynamodb:DescribeTable",
				"dynamodb:GetItem",
				"dynamodb:PutItem",
				"dynamodb:Query",
				"dynamodb:UpdateItem",
			},
			Resources: []string{r.BootstrapTable, r.BootstrapTablePart},
		},
		{
			Actions:   []string{"ssm:GetParameter", "ssm:GetParameters"},
			Resources: []string{r.passphraseParam, r.edgeParam, r.originParam, r.stackRecord},
		},
		{
			Actions:   []string{"ssm:DeleteParameter", "ssm:PutParameter"},
			Resources: []string{r.stackRecord},
		},
		{
			Actions:   []string{"kms:Decrypt", "kms:DescribeKey", "kms:Encrypt", "kms:GenerateDataKey"},
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VarsKeyComponentTagKey: VarsKeyComponentTagValue},
			},
		},
		{
			Actions:   []string{"kms:CreateGrant"},
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VarsKeyComponentTagKey: VarsKeyComponentTagValue},
				"Bool":         map[string]any{"kms:GrantIsForAWSResource": "true"},
			},
		},
		{
			Actions:   []string{"cloudformation:DescribeStacks"},
			Resources: []string{r.BootstrapStack},
		},
		{
			Actions:   []string{"sts:GetCallerIdentity"},
			Resources: []string{UnscopedResource},
		},
	}
}

func appProvisioning(ns Namespace, r ScopedARNs) []GrantStatement {
	return []GrantStatement{
		{
			Actions:   []string{"lambda:CreateFunction"},
			Resources: []string{appFunctionARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
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
			Resources: []string{appFunctionARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"iam:CreateRole"},
			Resources: []string{appRoleARN},
			Condition: mergeConditions(taggedOnCreate(), withinAppBoundary(ns)),
		},
		{
			Actions: []string{
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
			Resources: []string{appRoleARN},
			Condition: taggedByOcel(),
		},
		{
			Actions: []string{
				"iam:DeleteRolePolicy",
				"iam:PutRolePermissionsBoundary",
				"iam:PutRolePolicy",
			},
			Resources: []string{appRoleARN},
			Condition: mergeConditions(taggedByOcel(), withinAppBoundary(ns)),
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: []string{appRoleARN},
			Condition: mergeConditions(attachedPolicyIsAServiceRole(true), withinAppBoundary(ns)),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{appRoleARN},
			Condition: passedToLambda(true),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{appRoleARN},
			Condition: passedToECSTasks(),
		},
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{ecsLinkedRoleARN},
			Condition: linkedRoleFor("ecs.amazonaws.com"),
		},
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{elbLinkedRoleARN},
			Condition: linkedRoleFor("elasticloadbalancing.amazonaws.com"),
		},
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{vpcOriginLinkedRoleARN},
			Condition: linkedRoleFor("vpcorigin.cloudfront.amazonaws.com"),
		},
		{
			Actions:   []string{"cloudfront:CreateVpcOrigin"},
			Resources: []string{containerVPCOriginARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
				"cloudfront:DeleteVpcOrigin",
				"cloudfront:GetVpcOrigin",
				"cloudfront:ListTagsForResource",
				"cloudfront:TagResource",
				"cloudfront:UntagResource",
				"cloudfront:UpdateVpcOrigin",
			},
			Resources: []string{containerVPCOriginARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"ecr:GetAuthorizationToken"},
			Resources: []string{UnscopedResource},
		},
		{
			Actions: []string{
				"ecr:BatchCheckLayerAvailability",
				"ecr:BatchGetImage",
				"ecr:CompleteLayerUpload",
				"ecr:CreateRepository",
				"ecr:DescribeImages",
				"ecr:DescribeRepositories",
				"ecr:GetDownloadUrlForLayer",
				"ecr:InitiateLayerUpload",
				"ecr:ListTagsForResource",
				"ecr:PutImage",
				"ecr:TagResource",
				"ecr:UploadLayerPart",
			},
			Resources: []string{appRepositoryARN},
			Condition: inCallerAccount(),
		},
		{
			Actions:   []string{"ecs:CreateCluster"},
			Resources: []string{containerClusterARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
				"ecs:DeleteCluster",
				"ecs:DescribeClusters",
				"ecs:ListTagsForResource",
				"ecs:TagResource",
				"ecs:UntagResource",
			},
			Resources: []string{containerClusterARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"ecs:RegisterTaskDefinition"},
			Resources: []string{appTaskDefinitionARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"ecs:DeregisterTaskDefinition", "ecs:DescribeTaskDefinition"},
			Resources: []string{UnscopedResource},
		},
		{
			Actions:   []string{"ecs:CreateService"},
			Resources: []string{containerServiceARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
				"ecs:DeleteService",
				"ecs:DescribeServices",
				"ecs:ListTagsForResource",
				"ecs:TagResource",
				"ecs:UntagResource",
				"ecs:UpdateService",
			},
			Resources: []string{containerServiceARN},
			Condition: taggedByOcel(),
		},
		{
			Actions: []string{
				"elasticloadbalancing:DescribeListenerAttributes",
				"elasticloadbalancing:DescribeListeners",
				"elasticloadbalancing:DescribeLoadBalancerAttributes",
				"elasticloadbalancing:DescribeLoadBalancers",
				"elasticloadbalancing:DescribeRules",
				"elasticloadbalancing:DescribeTags",
				"elasticloadbalancing:DescribeTargetGroupAttributes",
				"elasticloadbalancing:DescribeTargetGroups",
				"elasticloadbalancing:DescribeTargetHealth",
			},
			Resources: []string{UnscopedResource},
		},
		{
			Actions:   []string{"elasticloadbalancing:CreateLoadBalancer", "elasticloadbalancing:CreateTargetGroup"},
			Resources: []string{containerBalancerARN, appTargetGroupARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
				"elasticloadbalancing:AddTags",
				"elasticloadbalancing:CreateListener",
				"elasticloadbalancing:CreateRule",
				"elasticloadbalancing:DeleteListener",
				"elasticloadbalancing:DeleteLoadBalancer",
				"elasticloadbalancing:DeleteRule",
				"elasticloadbalancing:DeleteTargetGroup",
				"elasticloadbalancing:ModifyListener",
				"elasticloadbalancing:ModifyListenerAttributes",
				"elasticloadbalancing:ModifyLoadBalancerAttributes",
				"elasticloadbalancing:ModifyRule",
				"elasticloadbalancing:ModifyTargetGroup",
				"elasticloadbalancing:ModifyTargetGroupAttributes",
				"elasticloadbalancing:RemoveTags",
				"elasticloadbalancing:SetSecurityGroups",
				"elasticloadbalancing:SetSubnets",
			},
			Resources: []string{containerBalancerARN, containerListenerARN, containerRuleARN, appTargetGroupARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"elasticloadbalancing:AddTags"},
			Resources: []string{containerBalancerARN, containerListenerARN, containerRuleARN, appTargetGroupARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"s3:CreateBucket"},
			Resources: []string{appBucketARN},
			Condition: inCallerAccount(),
		},
		{
			Actions: []string{
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
			Resources: []string{appBucketARN},
			Condition: inCallerAccount(),
		},
		{
			Actions: []string{
				"rds:AddTagsToResource",
				"rds:CreateDBCluster",
				"rds:CreateDBInstance",
				"rds:CreateDBSubnetGroup",
			},
			Resources: []string{appClusterARN, appInstanceARN, appSubnetGroupARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
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
			Resources: []string{appClusterARN, appInstanceARN, appSubnetGroupARN},
			Condition: taggedByOcel(),
		},
		{
			Actions: []string{
				"ec2:DescribeManagedPrefixLists",
				"ec2:DescribeNetworkInterfaces",
				"ec2:DescribeSecurityGroupRules",
				"ec2:DescribeSecurityGroups",
				"ec2:DescribeSubnets",
				"ec2:DescribeVpcs",
				"rds:DescribeDBEngineVersions",
				"rds:DescribeOrderableDBInstanceOptions",
			},
			Resources: []string{UnscopedResource},
		},
		{
			Actions:   []string{"ec2:CreateSecurityGroup"},
			Resources: []string{appSecurityGroupARN, appVPCARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"ec2:CreateTags"},
			Resources: []string{appSecurityGroupARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"ec2:CreateAction": "CreateSecurityGroup"},
			},
		},
		{
			Actions: []string{
				"ec2:AuthorizeSecurityGroupEgress",
				"ec2:AuthorizeSecurityGroupIngress",
				"ec2:DeleteSecurityGroup",
				"ec2:ModifySecurityGroupRules",
				"ec2:RevokeSecurityGroupEgress",
				"ec2:RevokeSecurityGroupIngress",
			},
			Resources: []string{appSecurityGroupARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"secretsmanager:DescribeSecret", "secretsmanager:GetSecretValue"},
			Resources: []string{appSecretARN},
			Condition: managedByAnAppCluster(),
		},
		{
			Actions:   []string{"logs:CreateLogGroup"},
			Resources: []string{appLogGroupARN, functionLogGroupARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
				"logs:DeleteLogGroup",
				"logs:ListTagsForResource",
				"logs:PutRetentionPolicy",
				"logs:TagResource",
				"logs:UntagResource",
			},
			Resources: []string{appLogGroupARN, functionLogGroupARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"logs:DescribeLogGroups"},
			Resources: []string{UnscopedResource},
		},
	}
}

func runtimeProvisioning(r ScopedARNs) []GrantStatement {
	return []GrantStatement{
		{
			Actions: []string{
				"lambda:DeleteLayerVersion",
				"lambda:GetLayerVersion",
				"lambda:ListLayerVersions",
				"lambda:PublishLayerVersion",
			},
			Resources: []string{r.runtimeLayer, r.runtimeLayerVersion},
		},
		{
			Actions: []string{
				"cloudformation:CreateChangeSet",
				"cloudformation:CreateStack",
				"cloudformation:DescribeStackEvents",
			},
			Resources: []string{r.runtimeStack},
		},
		{
			Actions: []string{
				"cloudformation:DeleteChangeSet",
				"cloudformation:DescribeChangeSet",
				"cloudformation:ExecuteChangeSet",
			},
			Resources: []string{r.runtimeStack, r.runtimeChangeSet},
		},
	}
}

func bootstrapProvisioning(ns Namespace, r ScopedARNs) []GrantStatement {
	return []GrantStatement{
		{
			Actions: []string{
				"cloudformation:CreateChangeSet",
				"cloudformation:CreateStack",
				"cloudformation:DeleteStack",
				"cloudformation:DescribeStackEvents",
			},
			Resources: []string{r.BootstrapStack},
		},
		{
			Actions: []string{
				"cloudformation:DeleteChangeSet",
				"cloudformation:DescribeChangeSet",
				"cloudformation:ExecuteChangeSet",
			},
			Resources: []string{r.BootstrapStack, r.bootstrapChangeSet},
		},
		{
			Actions:   []string{"s3:CreateBucket"},
			Resources: []string{r.bootstrapBucket},
		},
		{
			Actions: []string{
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
			Resources: []string{r.bootstrapBucket},
			Condition: inCallerAccount(),
		},
		{
			Actions:   []string{"s3:DeleteObjectVersion", "s3:GetObjectVersion"},
			Resources: []string{r.bootstrapObject},
			Condition: inCallerAccount(),
		},
		{
			Actions: []string{
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
			Resources: []string{r.BootstrapTable, r.BootstrapTablePart},
		},
		{
			Actions:   []string{"kms:CreateKey"},
			Resources: []string{UnscopedResource},
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:RequestTag/" + VarsKeyComponentTagKey: VarsKeyComponentTagValue},
			},
		},
		{
			Actions:   varsKeyLifecycleActions(),
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VarsKeyComponentTagKey: VarsKeyComponentTagValue},
			},
		},
		{
			Actions:   []string{"kms:DescribeKey", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus", "kms:ListResourceTags"},
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"ForAnyValue:StringLike": map[string]any{"kms:ResourceAliases": ns.varsKeyAliasFor("*")},
			},
		},
		{
			Actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			Resources: []string{r.varsAlias},
		},
		{
			Actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VarsKeyComponentTagKey: VarsKeyComponentTagValue},
			},
		},
		{
			Actions: []string{
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
			Resources: []string{r.appBoundary},
		},
		{
			Actions:   []string{"iam:DeleteRolePermissionsBoundary"},
			Resources: []string{appRoleARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"iam:CreateRole", "iam:TagRole"},
			Resources: []string{r.bootstrapRole},
		},
		{
			Actions: []string{
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
			Resources: []string{r.bootstrapRole},
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: []string{r.bootstrapRole},
			Condition: attachedPolicyIsAServiceRole(false),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{r.bootstrapRole},
			Condition: passedToLambda(false),
		},
		{
			Actions: []string{
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
			Resources: []string{r.bootstrapFunction},
		},
		{
			Actions: []string{
				"logs:CreateLogGroup",
				"logs:DeleteLogGroup",
				"logs:ListTagsForResource",
				"logs:PutRetentionPolicy",
				"logs:TagResource",
				"logs:UntagResource",
			},
			Resources: []string{r.bootstrapLogGroup},
		},
		{
			Actions:   []string{"lambda:CreateEventSourceMapping"},
			Resources: []string{UnscopedResource},
			Condition: map[string]any{
				"ArnLike": map[string]any{"lambda:FunctionArn": r.bootstrapFunction},
			},
		},
		{
			Actions: []string{
				"lambda:DeleteEventSourceMapping",
				"lambda:GetEventSourceMapping",
				"lambda:UpdateEventSourceMapping",
			},
			Resources: []string{bootstrapEventSourceARN},
			Condition: map[string]any{
				"ArnLike": map[string]any{"lambda:FunctionArn": r.bootstrapFunction},
			},
		},
		{
			Actions: []string{
				"sqs:CreateQueue",
				"sqs:DeleteQueue",
				"sqs:GetQueueAttributes",
				"sqs:GetQueueUrl",
				"sqs:ListQueueTags",
				"sqs:SetQueueAttributes",
				"sqs:TagQueue",
				"sqs:UntagQueue",
			},
			Resources: []string{r.bootstrapQueue},
		},
		{
			Actions:   []string{"ssm:AddTagsToResource", "ssm:PutParameter"},
			Resources: []string{r.anyParam},
		},
		{
			Actions:   []string{"ssm:DeleteParameter", "ssm:DeleteParameters"},
			Resources: []string{r.edgeParam, r.originParam, r.stackRecord},
		},
		{
			Actions:   []string{"ssm:DeleteParameter"},
			Resources: []string{r.passphraseParam},
		},
		{
			Actions:   []string{"ssm:GetParametersByPath"},
			Resources: []string{r.stackRecordTree, r.stackRecord},
		},
	}
}

func edgePrincipal(r ScopedARNs) []GrantStatement {
	return []GrantStatement{
		{
			Actions: []string{
				"iam:CreateAccessKey",
				"iam:CreateUser",
				"iam:DeleteAccessKey",
				"iam:DeleteUser",
				"iam:DeleteUserPolicy",
				"iam:GetAccessKeyLastUsed",
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
			Resources: []string{r.edgeUser},
		},
	}
}

func deployTier(ns Namespace) []GrantStatement {
	r := ns.ScopedARNs()
	return slices.Concat(bootstrapAccess(r), appProvisioning(ns, r), runtimeProvisioning(r))
}

func bootstrapTier(ns Namespace) []GrantStatement {
	r := ns.ScopedARNs()
	return slices.Concat(bootstrapAccess(r), appProvisioning(ns, r), runtimeProvisioning(r), bootstrapProvisioning(ns, r), edgePrincipal(r))
}

func DeployCredentialPermissions(ns Namespace) (string, error) {
	return credentialPolicy("deploy", deployTier(ns))
}

func BootstrapCredentialPermissions(ns Namespace) (string, error) {
	return credentialPolicy("bootstrap", bootstrapTier(ns))
}

func PolicyStatements(grants []GrantStatement) []map[string]any {
	statements := make([]map[string]any, 0, len(grants))
	for _, grant := range grants {
		statement := map[string]any{
			"Effect":   "Allow",
			"Action":   oneOrMany(grant.Actions),
			"Resource": oneOrMany(grant.Resources),
		}
		if len(grant.Condition) > 0 {
			statement["Condition"] = grant.Condition
		}
		statements = append(statements, statement)
	}
	return statements
}

func credentialPolicy(tier string, grants []GrantStatement) (string, error) {
	out, err := json.MarshalIndent(map[string]any{"Version": "2012-10-17", "Statement": PolicyStatements(grants)}, "", "  ")
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
