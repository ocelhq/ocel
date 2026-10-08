package bootstrap

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

const (
	callerAccount = "${aws:PrincipalAccount}"

	managedByTagKey   = "ocel:managed-by"
	managedByTagValue = "ocel"

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

	appBucketARN         = "arn:aws:s3:::" + appScopePrefix + "*"
	appFunctionARN       = "arn:aws:lambda:*:*:function:*"
	appWorkerFunctionARN = "arn:aws:lambda:*:*:function:" + appScopePrefix + "*"
	appRoleARN           = "arn:aws:iam::*:role/*"
	appSecretARN         = "arn:aws:secretsmanager:*:*:secret:rds!cluster-*"
	appClusterARN        = "arn:aws:rds:*:*:cluster:" + appScopePrefix + "*"
	appInstanceARN       = "arn:aws:rds:*:*:db:" + appScopePrefix + "*"
	appSubnetGroupARN    = "arn:aws:rds:*:*:subgrp:" + appScopePrefix + "*"

	appQueueARN         = "arn:aws:sqs:*:*:" + appScopePrefix + "*"
	appTopicARN         = "arn:aws:sns:*:*:" + appScopePrefix + "*"
	appSubscriptionARN  = "arn:aws:sns:*:*:" + appScopePrefix + "*:*"
	appScheduleGroupARN = "arn:aws:scheduler:*:*:schedule-group/" + appScopePrefix + "*"
	appScheduleARN      = "arn:aws:scheduler:*:*:schedule/" + appScopePrefix + "*/*"

	appReplicationGroupARN   = "arn:aws:elasticache:*:*:replicationgroup:" + appScopePrefix + "*"
	appCacheClusterARN       = "arn:aws:elasticache:*:*:cluster:" + appScopePrefix + "*"
	anyCacheClusterARN       = "arn:aws:elasticache:*:*:cluster:*"
	appCacheParameterARN     = "arn:aws:elasticache:*:*:parametergroup:" + appScopePrefix + "*"
	appCacheSubnetGroupARN   = "arn:aws:elasticache:*:*:subnetgroup:" + appScopePrefix + "*"
	elastiCacheLinkedRoleARN = "arn:aws:iam::*:role/aws-service-role/elasticache.amazonaws.com/*"

	appEventAPIARN         = "arn:aws:appsync:*:*:apis/*"
	appChannelNamespaceARN = "arn:aws:appsync:*:*:apis/*/channelNamespace/*"

	appSecurityGroupARN  = "arn:aws:ec2:*:*:security-group/*"
	appVPCARN            = "arn:aws:ec2:*:*:vpc/*"
	appRepositoryARN     = "arn:aws:ecr:*:*:repository/" + registry.Namespace + "/*"
	appLogGroupARN       = "arn:aws:logs:*:*:log-group:/ocel/*"
	functionLogGroupARN  = "arn:aws:logs:*:*:log-group:/aws/lambda/*"
	appTargetGroupARN    = "arn:aws:elasticloadbalancing:*:*:targetgroup/*/*"
	appTaskDefinitionARN = "arn:aws:ecs:*:*:task-definition/*:*"
	appCertificateARN    = "arn:aws:acm:*:*:certificate/*"

	managedSecretClusterTagKey = "aws:rds:primaryDBClusterArn"

	containerClusterARN     = "arn:aws:ecs:*:*:cluster/ocel-*"
	containerServiceARN     = "arn:aws:ecs:*:*:service/ocel-*/*"
	containerBalancerARN    = "arn:aws:elasticloadbalancing:*:*:loadbalancer/app/ocel-*/*"
	containerListenerARN    = "arn:aws:elasticloadbalancing:*:*:listener/app/ocel-*/*/*"
	containerRuleARN        = "arn:aws:elasticloadbalancing:*:*:listener-rule/app/ocel-*/*/*/*"
	containerTrustStoreARN  = "arn:aws:elasticloadbalancing:*:*:truststore/ocel-*/*"
	containerVPCOriginARN   = "arn:aws:cloudfront::*:vpcorigin/*"
	vpcOriginLinkedRoleARN  = "arn:aws:iam::*:role/aws-service-role/vpcorigin.cloudfront.amazonaws.com/*"
	ecsLinkedRoleARN        = "arn:aws:iam::*:role/aws-service-role/ecs.amazonaws.com/*"
	ecsScalingLinkedRoleARN = "arn:aws:iam::*:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/*"

	bastionClusterARN         = "arn:aws:ecs:*:*:cluster/" + bastion.Prefix + "-*"
	bastionTaskARN            = "arn:aws:ecs:*:*:task/" + bastion.Prefix + "-*/*"
	bastionTaskDefinitionARN  = "arn:aws:ecs:*:*:task-definition/" + bastion.Prefix + "-*:*"
	portForwardingDocumentARN = "arn:aws:ssm:*:*:document/" + bastion.PortForwardingDocument

	scalableTargetARN = "arn:aws:application-autoscaling:*:*:scalable-target/*"
	scalingAlarmARN   = "arn:aws:cloudwatch:*:*:alarm:TargetTracking-service/ocel-*"
	elbLinkedRoleARN  = "arn:aws:iam::*:role/aws-service-role/elasticloadbalancing.amazonaws.com/*"

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
	scheduleGroup       string
	schedule            string
	edgeUser            string
	passphraseParam     string
	edgeParam           string
	originParam         string
	stackRecordTree     string
	stackRecord         string
	anyParam            string
	kvToken             string
	signingKey          string
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
		scheduleGroup:      "arn:aws:scheduler:*:*:schedule-group/" + core + "*",
		schedule:           "arn:aws:scheduler:*:*:schedule/" + core + "*/*",
		edgeUser:           "arn:aws:iam::*:user/" + string(n) + "-edge*",
		passphraseParam:    parameterARNPrefix + n.PassphraseParamName(),
		edgeParam:          parameterARNPrefix + n.paramRoot() + "/edge/*",
		originParam:        parameterARNPrefix + n.paramRoot() + "/origin/*",
		stackRecordTree:    parameterARNPrefix + n.stackRecordRoot() + "*",
		anyParam:           parameterARNPrefix + n.paramRoot() + "/*",
		kvToken:            parameterARNPrefix + n.KVTokenRoot() + "/*",
		signingKey:         "arn:aws:secretsmanager:*:*:secret:" + n.SigningKeyRoot() + "/*",
	}
	a.bootstrapObject = a.bootstrapBucket + "/*"
	a.runtimeLayerVersion = a.runtimeLayer + ":*"
	a.BootstrapTablePart = a.BootstrapTable + "/*"
	a.stackRecord = a.stackRecordTree + "/*"
	return a
}

const (
	effectAllow = "Allow"
	effectDeny  = "Deny"
)

type GrantStatement struct {
	Effect    string
	Actions   []string
	Resources []string
	Condition map[string]any
}

func (n Namespace) tierStoresPrefix(tier environment.Tier) string {
	return suffixed(tier, n.CoreStackName())
}

func (n Namespace) ScopedARNsFor(tier environment.Tier) ScopedARNs {
	a := n.ScopedARNs()
	prefix := n.tierStoresPrefix(tier)
	a.bootstrapBucket = "arn:aws:s3:::" + prefix + "*"
	a.bootstrapObject = a.bootstrapBucket + "/*"
	a.BootstrapTable = "arn:aws:dynamodb:*:*:table/" + prefix + "*"
	a.BootstrapTablePart = a.BootstrapTable + "/*"
	return a
}

func withheldSiblingStores(ns Namespace, tier environment.Tier) []GrantStatement {
	sibling := ns.ScopedARNsFor(tier.Sibling())
	if tier != environment.TierProduction {
		return nil
	}
	return []GrantStatement{{
		Effect:    effectDeny,
		Actions:   []string{"s3:*", "dynamodb:*"},
		Resources: []string{sibling.bootstrapBucket, sibling.bootstrapObject, sibling.BootstrapTable, sibling.BootstrapTablePart},
	}}
}

func variablesKeyOfTier(ns Namespace, tier environment.Tier) map[string]any {
	return map[string]any{"ForAnyValue:StringEquals": map[string]any{"kms:ResourceAliases": ns.variablesKeyAliasFor(tier)}}
}

func policyARN(name string) string { return "arn:aws:iam::*:policy/" + name }

func aliasARN(alias string) string { return "arn:aws:kms:*:*:" + alias }

func inCallerAccount() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceAccount": callerAccount}}
}

const (
	principalProjectKey = "aws:PrincipalTag/" + registry.ProjectTag
	principalProject    = "${" + principalProjectKey + "}"
)

var principalProjectRepositoryARN = "arn:aws:ecr:*:*:repository/" + registry.Namespace + "/" + principalProject + images.ProjectSeparator + "*"

func principalUntagged() map[string]any {
	return map[string]any{"Null": map[string]any{principalProjectKey: "true"}}
}

func ofPrincipalProject() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/" + registry.ProjectTag: principalProject}}
}

func taggedForPrincipalProject() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:RequestTag/" + registry.ProjectTag: principalProject}}
}

func ofAProject() map[string]any {
	return map[string]any{"Null": map[string]any{"aws:ResourceTag/" + registry.ProjectTag: "false"}}
}

func taggedForAProject() map[string]any {
	return map[string]any{"Null": map[string]any{"aws:RequestTag/" + registry.ProjectTag: "false"}}
}

func taggedOnCreate() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:RequestTag/" + managedByTagKey: managedByTagValue}}
}

func taggedByOcel() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/" + managedByTagKey: managedByTagValue}}
}

func ofAppWorker() map[string]any {
	return map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": appWorkerFunctionARN}}
}

func keyPairSuppliedByCaller() map[string]any {
	return map[string]any{"StringEqualsIfExists": map[string]any{"acm:CertificateKeyPairOrigin": "CUSTOMER_PROVIDED"}}
}

func managedByAnAppCluster() map[string]any {
	return map[string]any{"StringLike": map[string]any{"aws:ResourceTag/" + managedSecretClusterTagKey: appClusterARN}}
}

func withinAppBoundary(ns Namespace, tier environment.Tier) map[string]any {
	return map[string]any{"StringEquals": map[string]any{
		"iam:PermissionsBoundary": appBoundaryARNFor(ns, tier),
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
		return mergeConditions(condition, taggedByOcel())
	}
	return condition
}

func passedToLambda(resourceTagged bool) map[string]any {
	return passedTo(LambdaServicePrincipal, resourceTagged)
}

func passedToECSTasks() map[string]any {
	return passedTo(ecsTasksPrincipal, true)
}

func scalesECSServices(tagged map[string]any) map[string]any {
	return mergeConditions(tagged, map[string]any{"StringEquals": map[string]any{
		"application-autoscaling:service-namespace":  "ecs",
		"application-autoscaling:scalable-dimension": "ecs:service:DesiredCount",
	}})
}

func linkedRoleFor(service string) map[string]any {
	return map[string]any{"StringEquals": map[string]any{"iam:AWSServiceName": service}}
}

func passedTo(service any, resourceTagged bool) map[string]any {
	condition := map[string]any{
		"StringEquals": map[string]any{"iam:PassedToService": service},
	}
	if resourceTagged {
		return mergeConditions(condition, taggedByOcel())
	}
	return condition
}

func variablesKeyLifecycleActions() []string {
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

func bootstrapAccess(ns Namespace, tier environment.Tier, r ScopedARNs) []GrantStatement {
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
			Condition: mergeConditions(variablesKeyOfTier(ns, tier), map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
			}),
		},
		{
			Actions:   []string{"kms:CreateGrant"},
			Resources: []string{AnyKeyARN},
			Condition: mergeConditions(variablesKeyOfTier(ns, tier), map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
				"Bool":         map[string]any{"kms:GrantIsForAWSResource": "true"},
			}),
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

func appProvisioning(ns Namespace, tier environment.Tier, r ScopedARNs) []GrantStatement {
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
			Condition: mergeConditions(taggedOnCreate(), withinAppBoundary(ns, tier)),
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
			Condition: mergeConditions(taggedByOcel(), withinAppBoundary(ns, tier)),
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: []string{appRoleARN},
			Condition: mergeConditions(attachedPolicyIsAServiceRole(true), withinAppBoundary(ns, tier)),
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
			Actions:   []string{"iam:PassRole"},
			Resources: []string{appRoleARN},
			Condition: passedTo(schedulerServicePrincipal, true),
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
			Resources: []string{appQueueARN},
		},
		{
			Actions: []string{
				"sns:CreateTopic",
				"sns:DeleteTopic",
				"sns:GetSubscriptionAttributes",
				"sns:GetTopicAttributes",
				"sns:ListTagsForResource",
				"sns:SetSubscriptionAttributes",
				"sns:SetTopicAttributes",
				"sns:Subscribe",
				"sns:TagResource",
				"sns:Unsubscribe",
				"sns:UntagResource",
			},
			Resources: []string{appTopicARN, appSubscriptionARN},
		},
		{
			Actions:   []string{"lambda:CreateEventSourceMapping"},
			Resources: []string{UnscopedResource},
			Condition: mergeConditions(taggedOnCreate(), ofAppWorker()),
		},
		{
			Actions: []string{
				"lambda:DeleteEventSourceMapping",
				"lambda:GetEventSourceMapping",
				"lambda:UpdateEventSourceMapping",
			},
			Resources: []string{bootstrapEventSourceARN},
			Condition: mergeConditions(taggedByOcel(), ofAppWorker()),
		},
		{
			Actions: []string{
				"lambda:ListTags",
				"lambda:TagResource",
				"lambda:UntagResource",
			},
			Resources: []string{bootstrapEventSourceARN},
			Condition: taggedByOcel(),
		},
		{
			Actions: []string{
				"scheduler:CreateScheduleGroup",
				"scheduler:DeleteScheduleGroup",
				"scheduler:GetScheduleGroup",
				"scheduler:ListTagsForResource",
				"scheduler:TagResource",
				"scheduler:UntagResource",
			},
			Resources: []string{appScheduleGroupARN},
		},
		{
			Actions: []string{
				"scheduler:CreateSchedule",
				"scheduler:DeleteSchedule",
				"scheduler:GetSchedule",
				"scheduler:UpdateSchedule",
			},
			Resources: []string{appScheduleARN},
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
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{elastiCacheLinkedRoleARN},
			Condition: linkedRoleFor("elasticache.amazonaws.com"),
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
				"ecr:DescribeImages",
				"ecr:DescribeRepositories",
				"ecr:GetDownloadUrlForLayer",
				"ecr:InitiateLayerUpload",
				"ecr:ListTagsForResource",
				"ecr:PutImage",
				"ecr:UploadLayerPart",
			},
			Resources: []string{appRepositoryARN},
			Condition: inCallerAccount(),
		},
		{
			Actions:   []string{"ecr:CreateRepository", "ecr:TagResource"},
			Resources: []string{principalProjectRepositoryARN},
			Condition: mergeConditions(inCallerAccount(), taggedForPrincipalProject()),
		},
		{
			Actions:   []string{"ecr:BatchDeleteImage", "ecr:DeleteRepository"},
			Resources: []string{principalProjectRepositoryARN},
			Condition: mergeConditions(inCallerAccount(), ofPrincipalProject()),
		},
		{
			Actions:   []string{"ecr:CreateRepository", "ecr:TagResource"},
			Resources: []string{appRepositoryARN},
			Condition: mergeConditions(inCallerAccount(), principalUntagged(), taggedForAProject()),
		},
		{
			Actions:   []string{"ecr:BatchDeleteImage", "ecr:DeleteRepository"},
			Resources: []string{appRepositoryARN},
			Condition: mergeConditions(inCallerAccount(), principalUntagged(), ofAProject()),
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
			Actions:   []string{"ecs:RunTask"},
			Resources: []string{bastionTaskDefinitionARN},
			Condition: map[string]any{"ArnEquals": map[string]any{"ecs:cluster": bastionClusterARN}},
		},
		{
			Actions:   []string{"ecs:DescribeTasks", "ecs:ExecuteCommand", "ecs:StopTask"},
			Resources: []string{bastionTaskARN},
		},
		{
			Actions:   []string{"ecs:DescribeClusters", "ecs:ExecuteCommand"},
			Resources: []string{bastionClusterARN},
		},
		{
			Actions:   []string{"ecs:ListTasks"},
			Resources: []string{UnscopedResource},
			Condition: map[string]any{"ArnEquals": map[string]any{"ecs:cluster": bastionClusterARN}},
		},
		{
			Actions:   []string{"ecs:DeleteTaskDefinitions"},
			Resources: []string{bastionTaskDefinitionARN},
		},
		{
			Actions:   []string{"ssm:StartSession"},
			Resources: []string{bastionTaskARN, portForwardingDocumentARN},
		},
		{
			Actions:   []string{"ecs:RegisterTaskDefinition"},
			Resources: []string{appTaskDefinitionARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"ecs:DeregisterTaskDefinition", "ecs:DescribeTaskDefinition", "ecs:ListTaskDefinitions"},
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
			Actions:   []string{"application-autoscaling:RegisterScalableTarget"},
			Resources: []string{scalableTargetARN},
			Condition: scalesECSServices(taggedOnCreate()),
		},
		{
			Actions: []string{
				"application-autoscaling:DeleteScalingPolicy",
				"application-autoscaling:DeregisterScalableTarget",
				"application-autoscaling:PutScalingPolicy",
				"application-autoscaling:RegisterScalableTarget",
			},
			Resources: []string{scalableTargetARN},
			Condition: scalesECSServices(taggedByOcel()),
		},
		{
			Actions:   []string{"application-autoscaling:TagResource"},
			Resources: []string{scalableTargetARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"application-autoscaling:TagResource", "application-autoscaling:UntagResource"},
			Resources: []string{scalableTargetARN},
			Condition: taggedByOcel(),
		},
		{
			Actions: []string{
				"application-autoscaling:DescribeScalableTargets",
				"application-autoscaling:DescribeScalingActivities",
				"application-autoscaling:DescribeScalingPolicies",
				"application-autoscaling:ListTagsForResource",
				"cloudwatch:DescribeAlarms",
			},
			Resources: []string{UnscopedResource},
		},
		{
			Actions:   []string{"cloudwatch:DeleteAlarms", "cloudwatch:PutMetricAlarm"},
			Resources: []string{scalingAlarmARN},
		},
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{ecsScalingLinkedRoleARN},
			Condition: linkedRoleFor("ecs.application-autoscaling.amazonaws.com"),
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
				"elasticloadbalancing:DescribeListenerCertificates",
				"elasticloadbalancing:DescribeTrustStores",
			},
			Resources: []string{UnscopedResource},
		},
		{
			Actions:   []string{"elasticloadbalancing:CreateLoadBalancer", "elasticloadbalancing:CreateTargetGroup", "elasticloadbalancing:CreateTrustStore"},
			Resources: []string{containerBalancerARN, appTargetGroupARN, containerTrustStoreARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
				"elasticloadbalancing:AddListenerCertificates",
				"elasticloadbalancing:RemoveListenerCertificates",
			},
			Resources: []string{containerListenerARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"elasticloadbalancing:AddTags", "elasticloadbalancing:ModifyTrustStore", "elasticloadbalancing:DeleteTrustStore"},
			Resources: []string{containerTrustStoreARN},
			Condition: taggedByOcel(),
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
			Resources: []string{containerBalancerARN, containerListenerARN, containerRuleARN, appTargetGroupARN, containerTrustStoreARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"acm:ImportCertificate"},
			Resources: []string{appCertificateARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"acm:AddTagsToCertificate"},
			Resources: []string{appCertificateARN},
			Condition: mergeConditions(taggedOnCreate(), keyPairSuppliedByCaller()),
		},
		{
			Actions:   []string{"acm:DeleteCertificate"},
			Resources: []string{appCertificateARN},
			Condition: mergeConditions(taggedByOcel(), keyPairSuppliedByCaller()),
		},
		{
			Actions: []string{
				"acm:AddTagsToCertificate",
				"acm:DescribeCertificate",
				"acm:ListTagsForCertificate",
				"acm:RemoveTagsFromCertificate",
			},
			Resources: []string{appCertificateARN},
			Condition: taggedByOcel(),
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
				"elasticache:AddTagsToResource",
				"elasticache:CreateReplicationGroup",
				"elasticache:ListTagsForResource",
				"elasticache:RemoveTagsFromResource",
			},
			Resources: []string{appReplicationGroupARN, appCacheClusterARN, appCacheParameterARN, appCacheSubnetGroupARN},
		},
		{
			Actions:   []string{"elasticache:ModifyReplicationGroup"},
			Resources: []string{appReplicationGroupARN, appCacheParameterARN},
		},
		{
			Actions: []string{
				"elasticache:DecreaseReplicaCount",
				"elasticache:DeleteReplicationGroup",
				"elasticache:DescribeReplicationGroups",
				"elasticache:IncreaseReplicaCount",
			},
			Resources: []string{appReplicationGroupARN},
		},
		{
			Actions:   []string{"elasticache:DescribeCacheClusters"},
			Resources: []string{anyCacheClusterARN},
		},
		{
			Actions: []string{
				"elasticache:CreateCacheParameterGroup",
				"elasticache:DeleteCacheParameterGroup",
				"elasticache:DescribeCacheParameterGroups",
				"elasticache:DescribeCacheParameters",
				"elasticache:ModifyCacheParameterGroup",
			},
			Resources: []string{appCacheParameterARN},
		},
		{
			Actions: []string{
				"elasticache:CreateCacheSubnetGroup",
				"elasticache:DeleteCacheSubnetGroup",
				"elasticache:DescribeCacheSubnetGroups",
				"elasticache:ModifyCacheSubnetGroup",
			},
			Resources: []string{appCacheSubnetGroupARN},
		},
		{
			Actions:   []string{"ssm:DeleteParameter", "ssm:GetParameter", "ssm:GetParametersByPath", "ssm:PutParameter"},
			Resources: []string{r.kvToken},
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
			Actions:   []string{"secretsmanager:CreateSecret", "secretsmanager:TagResource"},
			Resources: []string{r.signingKey},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"secretsmanager:DeleteSecret", "secretsmanager:GetSecretValue"},
			Resources: []string{r.signingKey},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"secretsmanager:ListSecrets"},
			Resources: []string{UnscopedResource},
		},
		{
			Actions:   []string{"appsync:CreateApi"},
			Resources: []string{UnscopedResource},
			Condition: taggedOnCreate(),
		},
		{
			Actions:   []string{"appsync:CreateChannelNamespace"},
			Resources: []string{appChannelNamespaceARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
				"appsync:DeleteApi",
				"appsync:DeleteChannelNamespace",
				"appsync:GetApi",
				"appsync:GetChannelNamespace",
				"appsync:ListTagsForResource",
				"appsync:TagResource",
				"appsync:UntagResource",
				"appsync:UpdateApi",
				"appsync:UpdateChannelNamespace",
			},
			Resources: []string{appEventAPIARN, appChannelNamespaceARN},
			Condition: taggedByOcel(),
		},
		{
			Actions:   []string{"logs:CreateLogGroup"},
			Resources: []string{appLogGroupARN, functionLogGroupARN},
			Condition: taggedOnCreate(),
		},
		{
			Actions: []string{
				"logs:DeleteLogGroup",
				"logs:FilterLogEvents",
				"logs:ListTagsForResource",
				"logs:PutRetentionPolicy",
				"logs:StartLiveTail",
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

func bootstrapProvisioning(ns Namespace, tier environment.Tier, r ScopedARNs) []GrantStatement {
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
				"StringEquals": map[string]any{"aws:RequestTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
			},
		},
		{
			Actions:   variablesKeyLifecycleActions(),
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
			},
		},
		{
			Actions:   []string{"kms:DescribeKey", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus", "kms:ListResourceTags"},
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"ForAnyValue:StringEquals": map[string]any{"kms:ResourceAliases": ns.variablesKeyAliasFor(tier)},
			},
		},
		{
			Actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			Resources: []string{aliasARN(ns.variablesKeyAliasFor(tier))},
		},
		{
			Actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
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
			Resources: []string{policyARN(ns.AppBoundaryNameFor(tier))},
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
			Actions:   []string{"iam:PassRole"},
			Resources: []string{r.bootstrapRole},
			Condition: passedTo(schedulerServicePrincipal, false),
		},
		{
			Actions: []string{
				"scheduler:CreateScheduleGroup",
				"scheduler:DeleteScheduleGroup",
				"scheduler:GetScheduleGroup",
				"scheduler:ListTagsForResource",
				"scheduler:TagResource",
				"scheduler:UntagResource",
			},
			Resources: []string{r.scheduleGroup},
		},
		{
			Actions: []string{
				"scheduler:CreateSchedule",
				"scheduler:DeleteSchedule",
				"scheduler:GetSchedule",
				"scheduler:UpdateSchedule",
			},
			Resources: []string{r.schedule},
		},
		{
			Actions: []string{
				"lambda:AddPermission",
				"lambda:CreateFunction",
				"lambda:CreateFunctionUrlConfig",
				"lambda:DeleteFunction",
				"lambda:DeleteFunctionEventInvokeConfig",
				"lambda:DeleteFunctionUrlConfig",
				"lambda:GetFunction",
				"lambda:GetFunctionConfiguration",
				"lambda:GetFunctionEventInvokeConfig",
				"lambda:GetFunctionUrlConfig",
				"lambda:GetPolicy",
				"lambda:ListTags",
				"lambda:PutFunctionEventInvokeConfig",
				"lambda:RemovePermission",
				"lambda:TagResource",
				"lambda:UntagResource",
				"lambda:UpdateFunctionCode",
				"lambda:UpdateFunctionConfiguration",
				"lambda:UpdateFunctionEventInvokeConfig",
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

func deployGrants(ns Namespace, tier environment.Tier) []GrantStatement {
	r := ns.ScopedARNsFor(tier)
	return slices.Concat(bootstrapAccess(ns, tier, r), appProvisioning(ns, tier, r), runtimeProvisioning(r), withheldSiblingStores(ns, tier))
}

func bootstrapGrants(ns Namespace, tier environment.Tier) []GrantStatement {
	r := ns.ScopedARNsFor(tier)
	return slices.Concat(bootstrapAccess(ns, tier, r), appProvisioning(ns, tier, r), runtimeProvisioning(r), bootstrapProvisioning(ns, tier, r), edgePrincipal(r), withheldSiblingStores(ns, tier))
}

func refuseUnservedTier(tier environment.Tier) error {
	if tier == environment.TierProduction || tier == environment.TierPreview {
		return nil
	}
	return fmt.Errorf("credential permissions are rendered for the production or preview tier, not %q", tier)
}

func DeployCredentialPermissions(ns Namespace, tier environment.Tier) (string, error) {
	if err := refuseUnservedTier(tier); err != nil {
		return "", err
	}
	return credentialPolicy("deploy", deployGrants(ns, tier))
}

func BootstrapCredentialPermissions(ns Namespace, tier environment.Tier) (string, error) {
	if err := refuseUnservedTier(tier); err != nil {
		return "", err
	}
	return credentialPolicy("bootstrap", bootstrapGrants(ns, tier))
}

func PolicyStatements(grants []GrantStatement) []map[string]any {
	statements := make([]map[string]any, 0, len(grants))
	for _, grant := range grants {
		statement := map[string]any{
			"Effect":   cmp.Or(grant.Effect, effectAllow),
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

func credentialPolicy(purpose string, grants []GrantStatement) (string, error) {
	out, err := json.MarshalIndent(map[string]any{"Version": "2012-10-17", "Statement": PolicyStatements(grants)}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render the %s credential policy: %w", purpose, err)
	}
	return string(out), nil
}

func oneOrMany(values []string) any {
	if len(values) == 1 {
		return values[0]
	}
	return values
}
