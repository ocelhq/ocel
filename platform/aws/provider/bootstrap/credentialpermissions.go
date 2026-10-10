package bootstrap

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
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
	tier                 environment.Tier
	variablesKeyAlias    string
	appBoundary          string
	appBoundaryPolicy    string
	bootstrapBuckets     []string
	bootstrapObjects     []string
	BootstrapTables      []string
	BootstrapStacks      []string
	bootstrapChangeSets  []string
	runtimeStack         string
	runtimeChangeSet     string
	runtimeLayers        []string
	runtimeLayerVersions []string
	bootstrapRoles       []string
	appRoles             string
	bastionRole          string
	bootstrapFunction    string
	bootstrapLogGroups   []string
	bootstrapQueues      []string
	scheduleGroup        string
	schedule             string
	edgeUser             string
	passphraseParam      string
	edgeParams           []string
	originParam          string
	kvToken              string
	signingKey           string
}

var (
	coreStackBuckets = []string{"StateBucket", "ArtifactBucket", "AssetBucket"}
	coreStackTables  = []string{"StateTable", "VariablesTable"}
)

func (n Namespace) ScopedARNs(tier environment.Tier) ScopedARNs {
	core, _ := n.StackNameFor(tier)
	runtime := n.runtimeStackName(tier)
	group := n.envSourceSyncScheduleGroupName(tier)
	edgeUser, _ := n.EdgeUserNameFor(tier)
	origin, _ := n.OriginSecretParamFor(tier)
	a := ScopedARNs{
		tier:              tier,
		variablesKeyAlias: n.variablesKeyAliasFor(tier),
		appBoundary:       appBoundaryARNFor(n, tier),
		appBoundaryPolicy: policyARN(n.AppBoundaryNameFor(tier)),
		runtimeStack:      "arn:aws:cloudformation:*:*:stack/" + runtime + "/*",
		runtimeChangeSet:  "arn:aws:cloudformation:*:*:changeSet/" + runtime + "-*/*",
		bootstrapRoles:    []string{"arn:aws:iam::*:role" + n.bootstrapRolePathFor(tier) + "*", "arn:aws:iam::*:role/" + n.EdgeInvokeRoleName(tier)},
		appRoles:          "arn:aws:iam::*:role" + n.AppRolePathFor(tier) + "*",
		bastionRole:       "arn:aws:iam::*:role/" + bastion.NameFor(tier),
		bootstrapFunction: "arn:aws:lambda:*:*:function:" + n.CoreStackName() + "*",
		scheduleGroup:     "arn:aws:scheduler:*:*:schedule-group/" + group,
		schedule:          "arn:aws:scheduler:*:*:schedule/" + group + "/*",
		edgeUser:          "arn:aws:iam::*:user/" + edgeUser,
		passphraseParam:   parameterARNPrefix + n.PassphraseParamFor(tier),
		originParam:       parameterARNPrefix + origin,
		kvToken:           parameterARNPrefix + n.KVTokenRoot() + "/*",
		signingKey:        "arn:aws:secretsmanager:*:*:secret:" + n.SigningKeyRoot() + "/*",
	}
	for _, bucket := range coreStackBuckets {
		named := "arn:aws:s3:::" + strings.ToLower(core+"-"+bucket) + "-*"
		a.bootstrapBuckets = append(a.bootstrapBuckets, named)
		a.bootstrapObjects = append(a.bootstrapObjects, named+"/*")
	}
	for _, table := range coreStackTables {
		named := "arn:aws:dynamodb:*:*:table/" + core + "-" + table + "-*"
		a.BootstrapTables = append(a.BootstrapTables, named, named+"/*")
	}
	queue, deadLetters := n.revalidateQueueNames(tier)
	a.bootstrapQueues = []string{"arn:aws:sqs:*:*:" + queue, "arn:aws:sqs:*:*:" + deadLetters}
	stacks := []string{core, runtime}
	for _, f := range featureRegistry {
		stack := f.stackName(n, tier)
		stacks = append(stacks, stack)
		a.bootstrapLogGroups = append(a.bootstrapLogGroups, "arn:aws:logs:*:*:log-group:/aws/lambda/"+stack+"-*")
		a.bootstrapQueues = append(a.bootstrapQueues, "arn:aws:sqs:*:*:"+stack+"-*")
	}
	for _, stack := range stacks {
		a.BootstrapStacks = append(a.BootstrapStacks, "arn:aws:cloudformation:*:*:stack/"+stack+"/*")
		a.bootstrapChangeSets = append(a.bootstrapChangeSets, "arn:aws:cloudformation:*:*:changeSet/"+stack+"-*/*")
	}
	for _, token := range slices.Sorted(maps.Values(runtimeArchTokens)) {
		layer := "arn:aws:lambda:*:*:layer:" + runtimeLayerPrefix(n, tier, token) + "*"
		a.runtimeLayers = append(a.runtimeLayers, layer)
		a.runtimeLayerVersions = append(a.runtimeLayerVersions, layer+":*")
	}
	for _, kind := range edgeKinds() {
		prefix, _ := n.EdgeParamPrefix(tier, kind)
		a.edgeParams = append(a.edgeParams, parameterARNPrefix+prefix+"/*")
	}
	return a
}

type GrantStatement struct {
	Actions   []string
	Resources []string
	Condition map[string]any
}

func (r ScopedARNs) variablesKey() map[string]any {
	return map[string]any{"ForAnyValue:StringEquals": map[string]any{"kms:ResourceAliases": r.variablesKeyAlias}}
}

func (r ScopedARNs) withinAppBoundary() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"iam:PermissionsBoundary": r.appBoundary}}
}

func (r ScopedARNs) madeByItsStacks() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/" + naming.EnvTierTagKey: string(r.tier)}}
}

func (r ScopedARNs) madeForItsTier() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:RequestTag/" + naming.EnvTierTagKey: string(r.tier)}}
}

func (r ScopedARNs) taggedOnlyAsItsTier() map[string]any {
	return map[string]any{"StringEqualsIfExists": map[string]any{
		"aws:ResourceTag/" + naming.EnvTierTagKey: string(r.tier),
		"aws:RequestTag/" + naming.EnvTierTagKey:  string(r.tier),
	}}
}

func (r ScopedARNs) taggedOnCreateForItsTier() map[string]any {
	return mergeConditions(taggedOnCreate(), r.madeForItsTier())
}

func (r ScopedARNs) taggedByOcelForItsTier() map[string]any {
	return mergeConditions(taggedByOcel(), r.madeByItsStacks())
}

func (r ScopedARNs) retaggedByOcelOnlyAsItsTier() map[string]any {
	return mergeConditions(r.taggedByOcelForItsTier(), r.retaggedOnlyAsItsTier())
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

func passedToLambda() map[string]any {
	return passedTo(LambdaServicePrincipal)
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

func passedTo(service any) map[string]any {
	return map[string]any{"StringEquals": map[string]any{"iam:PassedToService": service}}
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
		"kms:UntagResource",
	}
}

func (r ScopedARNs) itsVariablesKey() map[string]any {
	return map[string]any{"StringEquals": map[string]any{
		"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue,
		"aws:ResourceTag/" + naming.EnvTierTagKey:        string(r.tier),
	}}
}

func (r ScopedARNs) retaggedOnlyAsItsTier() map[string]any {
	return map[string]any{"StringEqualsIfExists": map[string]any{"aws:RequestTag/" + naming.EnvTierTagKey: string(r.tier)}}
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
			Resources: r.bootstrapObjects,
			Condition: inCallerAccount(),
		},
		{
			Actions:   []string{"s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"},
			Resources: r.bootstrapBuckets,
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
			Resources: r.BootstrapTables,
		},
		{
			Actions:   []string{"ssm:GetParameter", "ssm:GetParameters"},
			Resources: slices.Concat([]string{r.passphraseParam, r.originParam}, r.edgeParams),
		},
		{
			Actions:   []string{"kms:Decrypt", "kms:DescribeKey", "kms:Encrypt", "kms:GenerateDataKey"},
			Resources: []string{AnyKeyARN},
			Condition: mergeConditions(r.variablesKey(), map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
			}),
		},
		{
			Actions:   []string{"kms:CreateGrant"},
			Resources: []string{AnyKeyARN},
			Condition: mergeConditions(r.variablesKey(), map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
				"Bool":         map[string]any{"kms:GrantIsForAWSResource": "true"},
			}),
		},
		{
			Actions:   []string{"cloudformation:DescribeStacks"},
			Resources: r.BootstrapStacks,
		},
		{
			Actions:   []string{"sts:GetCallerIdentity"},
			Resources: []string{UnscopedResource},
		},
	}
}

func appProvisioning(r ScopedARNs) []GrantStatement {
	return []GrantStatement{
		{
			Actions:   []string{"lambda:CreateFunction"},
			Resources: []string{appFunctionARN},
			Condition: r.taggedOnCreateForItsTier(),
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
				"lambda:UntagResource",
				"lambda:UpdateFunctionCode",
				"lambda:UpdateFunctionConfiguration",
				"lambda:UpdateFunctionUrlConfig",
			},
			Resources: []string{appFunctionARN},
			Condition: r.taggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"lambda:TagResource"},
			Resources: []string{appFunctionARN},
			Condition: r.retaggedByOcelOnlyAsItsTier(),
		},
		{
			Actions:   []string{"iam:CreateRole"},
			Resources: []string{appRoleARN},
			Condition: mergeConditions(r.taggedOnCreateForItsTier(), r.withinAppBoundary()),
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
				"iam:UntagRole",
				"iam:UpdateRole",
			},
			Resources: []string{appRoleARN},
			Condition: r.taggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"iam:TagRole"},
			Resources: []string{appRoleARN},
			Condition: r.retaggedByOcelOnlyAsItsTier(),
		},
		{
			Actions: []string{
				"iam:DeleteRolePolicy",
				"iam:PutRolePermissionsBoundary",
				"iam:PutRolePolicy",
			},
			Resources: []string{appRoleARN},
			Condition: mergeConditions(r.taggedByOcelForItsTier(), r.withinAppBoundary()),
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: []string{appRoleARN},
			Condition: mergeConditions(attachedPolicyIsAServiceRole(true), r.withinAppBoundary(), r.madeByItsStacks()),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{r.appRoles},
			Condition: passedToLambda(),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{r.appRoles, r.bastionRole},
			Condition: passedTo(ecsTasksPrincipal),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{r.appRoles},
			Condition: passedTo(schedulerServicePrincipal),
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
			Condition: mergeConditions(r.taggedOnCreateForItsTier(), ofAppWorker()),
		},
		{
			Actions: []string{
				"lambda:DeleteEventSourceMapping",
				"lambda:GetEventSourceMapping",
				"lambda:UpdateEventSourceMapping",
			},
			Resources: []string{bootstrapEventSourceARN},
			Condition: mergeConditions(r.taggedByOcelForItsTier(), ofAppWorker()),
		},
		{
			Actions:   []string{"lambda:ListTags", "lambda:UntagResource"},
			Resources: []string{bootstrapEventSourceARN},
			Condition: r.taggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"lambda:TagResource"},
			Resources: []string{bootstrapEventSourceARN},
			Condition: r.retaggedByOcelOnlyAsItsTier(),
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
			Resources: slices.Concat(r.runtimeLayers, r.runtimeLayerVersions),
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

func bootstrapProvisioning(r ScopedARNs) []GrantStatement {
	return []GrantStatement{
		{
			Actions: []string{
				"cloudformation:CreateChangeSet",
				"cloudformation:CreateStack",
				"cloudformation:DeleteStack",
				"cloudformation:DescribeStackEvents",
			},
			Resources: r.BootstrapStacks,
		},
		{
			Actions: []string{
				"cloudformation:DeleteChangeSet",
				"cloudformation:DescribeChangeSet",
				"cloudformation:ExecuteChangeSet",
			},
			Resources: slices.Concat(r.BootstrapStacks, r.bootstrapChangeSets),
		},
		{
			Actions:   []string{"s3:CreateBucket"},
			Resources: r.bootstrapBuckets,
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
			Resources: r.bootstrapBuckets,
			Condition: inCallerAccount(),
		},
		{
			Actions:   []string{"s3:DeleteObjectVersion", "s3:GetObjectVersion"},
			Resources: r.bootstrapObjects,
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
			Resources: r.BootstrapTables,
		},
		{
			Actions:   []string{"kms:CreateKey"},
			Resources: []string{UnscopedResource},
			Condition: map[string]any{"StringEquals": map[string]any{
				"aws:RequestTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue,
				"aws:RequestTag/" + naming.EnvTierTagKey:        string(r.tier),
			}},
		},
		{
			Actions:   variablesKeyLifecycleActions(),
			Resources: []string{AnyKeyARN},
			Condition: r.itsVariablesKey(),
		},
		{
			Actions:   []string{"kms:TagResource"},
			Resources: []string{AnyKeyARN},
			Condition: mergeConditions(r.itsVariablesKey(), r.retaggedOnlyAsItsTier()),
		},
		{
			Actions:   []string{"kms:DescribeKey", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus", "kms:ListResourceTags"},
			Resources: []string{AnyKeyARN},
			Condition: map[string]any{
				"ForAnyValue:StringEquals": map[string]any{"kms:ResourceAliases": r.variablesKeyAlias},
			},
		},
		{
			Actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			Resources: []string{aliasARN(r.variablesKeyAlias)},
		},
		{
			Actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			Resources: []string{AnyKeyARN},
			Condition: r.itsVariablesKey(),
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
			Resources: []string{r.appBoundaryPolicy},
		},
		{
			Actions:   []string{"iam:DeleteRolePermissionsBoundary"},
			Resources: []string{appRoleARN},
			Condition: r.taggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"iam:CreateRole"},
			Resources: r.bootstrapRoles,
			Condition: r.madeForItsTier(),
		},
		{
			Actions:   []string{"iam:TagRole"},
			Resources: r.bootstrapRoles,
			Condition: r.taggedOnlyAsItsTier(),
		},
		{
			Actions: []string{
				"iam:GetRole",
				"iam:GetRolePolicy",
				"iam:ListAttachedRolePolicies",
				"iam:ListRolePolicies",
				"iam:ListRoleTags",
			},
			Resources: r.bootstrapRoles,
		},
		{
			Actions: []string{
				"iam:DeleteRole",
				"iam:DeleteRolePolicy",
				"iam:PutRolePolicy",
				"iam:UntagRole",
				"iam:UpdateAssumeRolePolicy",
				"iam:UpdateRole",
			},
			Resources: r.bootstrapRoles,
			Condition: r.madeByItsStacks(),
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: r.bootstrapRoles,
			Condition: mergeConditions(attachedPolicyIsAServiceRole(false), r.madeByItsStacks()),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: r.bootstrapRoles,
			Condition: passedToLambda(),
		},
		{
			Actions:   []string{"iam:PassRole"},
			Resources: r.bootstrapRoles,
			Condition: passedTo(schedulerServicePrincipal),
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
			Actions:   []string{"lambda:CreateFunction"},
			Resources: []string{r.bootstrapFunction},
			Condition: r.madeForItsTier(),
		},
		{
			Actions:   []string{"lambda:TagResource"},
			Resources: []string{r.bootstrapFunction},
			Condition: r.taggedOnlyAsItsTier(),
		},
		{
			Actions: []string{
				"lambda:GetFunction",
				"lambda:GetFunctionConfiguration",
				"lambda:GetFunctionEventInvokeConfig",
				"lambda:GetFunctionUrlConfig",
				"lambda:GetPolicy",
				"lambda:ListTags",
			},
			Resources: []string{r.bootstrapFunction},
		},
		{
			Actions: []string{
				"lambda:AddPermission",
				"lambda:CreateFunctionUrlConfig",
				"lambda:DeleteFunction",
				"lambda:DeleteFunctionEventInvokeConfig",
				"lambda:DeleteFunctionUrlConfig",
				"lambda:PutFunctionEventInvokeConfig",
				"lambda:RemovePermission",
				"lambda:UntagResource",
				"lambda:UpdateFunctionCode",
				"lambda:UpdateFunctionConfiguration",
				"lambda:UpdateFunctionEventInvokeConfig",
				"lambda:UpdateFunctionUrlConfig",
			},
			Resources: []string{r.bootstrapFunction},
			Condition: r.madeByItsStacks(),
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
			Resources: r.bootstrapLogGroups,
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
			Resources: r.bootstrapQueues,
		},
		{
			Actions:   []string{"ssm:AddTagsToResource", "ssm:PutParameter"},
			Resources: slices.Concat([]string{r.passphraseParam, r.originParam}, r.edgeParams),
		},
		{
			Actions:   []string{"ssm:DeleteParameter", "ssm:DeleteParameters"},
			Resources: slices.Concat([]string{r.originParam}, r.edgeParams),
		},
		{
			Actions:   []string{"ssm:DeleteParameter"},
			Resources: []string{r.passphraseParam},
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

func broughtVariablesKeyAccess(broughtKey string) []GrantStatement {
	if broughtKey == "" {
		return nil
	}
	return []GrantStatement{
		{
			Actions:   []string{"kms:Decrypt", "kms:DescribeKey", "kms:Encrypt"},
			Resources: []string{broughtKey},
		},
		{
			Actions:   []string{"kms:CreateGrant"},
			Resources: []string{broughtKey},
			Condition: map[string]any{
				"Bool": map[string]any{"kms:GrantIsForAWSResource": "true"},
			},
		},
	}
}

func deployGrants(r ScopedARNs, broughtKey string) []GrantStatement {
	return slices.Concat(bootstrapAccess(r), broughtVariablesKeyAccess(broughtKey), appProvisioning(r), runtimeProvisioning(r))
}

func bootstrapGrants(r ScopedARNs, broughtKey string) []GrantStatement {
	return slices.Concat(bootstrapAccess(r), broughtVariablesKeyAccess(broughtKey), appProvisioning(r), runtimeProvisioning(r), bootstrapProvisioning(r), edgePrincipal(r))
}

func refuseUnservedTier(tier environment.Tier) error {
	if tier == environment.TierProduction || tier == environment.TierPreview {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid, "credential permissions are rendered for the production or preview tier, not %q", tier)
}

func DeployCredentialPermissions(ns Namespace, tier environment.Tier, broughtKey string) (string, error) {
	if err := refuseUnservedTier(tier); err != nil {
		return "", err
	}
	return credentialPolicy("deploy", deployGrants(ns.ScopedARNs(tier), broughtKey))
}

func BootstrapCredentialPermissions(ns Namespace, tier environment.Tier, broughtKey string) (string, error) {
	if err := refuseUnservedTier(tier); err != nil {
		return "", err
	}
	return credentialPolicy("bootstrap", bootstrapGrants(ns.ScopedARNs(tier), broughtKey))
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
