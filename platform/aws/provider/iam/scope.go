package iam

import (
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

type Scope struct {
	Namespace string
	Tier      environment.Tier

	VariablesKeyAlias   string
	BroughtVariablesKey string

	AppBoundary       string
	AppBoundaryPolicy string
	AppRoles          string

	BootstrapBuckets    []string
	BootstrapObjects    []string
	BootstrapTables     []string
	BootstrapStacks     []string
	BootstrapChangeSets []string
	BootstrapRoles      []string
	PartRoles           []string
	DeployPolicies      string

	RuntimeLayers        []string
	RuntimeLayerVersions []string

	BastionRole                string
	TierFunctions              []string
	TierFunctionLogGroups      []string
	EnvSourceSyncFunction      string
	EnvSourceSyncScheduleGroup string
	EnvSourceSyncSchedule      string
	TagInvalidatorQueue        string
	TagInvalidatorRole         string
	TagInvalidatorFunction     string
	EdgeFunctions              []string
	EdgeUser                   string

	PassphraseParameter string
	OriginParameter     string
	EdgeParameters      []string
	KVTokens            string
	SigningKeys         string
}

func (s Scope) AliasedAsItsVariablesKey() map[string]any {
	return map[string]any{"ForAnyValue:StringEquals": map[string]any{"kms:ResourceAliases": s.VariablesKeyAlias}}
}

func (s Scope) WithinAppBoundary() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"iam:PermissionsBoundary": s.AppBoundary}}
}

func (s Scope) appFenced(arnPrefix, suffix string) string {
	return arnPrefix + naming.AppFence(s.Namespace) + "*" + suffix
}

func (s Scope) AppBucketARN() string { return s.appFenced("arn:aws:s3:::", "") }

func (s Scope) AppFunctionARN() string { return s.appFenced("arn:aws:lambda:*:*:function:", "") }

func (s Scope) FunctionARNs() []string {
	return append([]string{s.AppFunctionARN()}, s.TierFunctions...)
}

func (s Scope) FunctionLogGroupARNs() []string {
	return append([]string{s.appFenced("arn:aws:logs:*:*:log-group:/aws/lambda/", "")}, s.TierFunctionLogGroups...)
}

func (s Scope) AppBoundaryRoles() []string { return []string{s.AppRoles, s.BastionRole} }

func (s Scope) tierFenced(arnPrefix string) string {
	return arnPrefix + naming.AppFence(s.Namespace) + naming.TierWord(s.Tier) + naming.WordSeparator + "*"
}

func (s Scope) AppClusterARN() string { return s.tierFenced("arn:aws:rds:*:*:cluster:") }

func (s Scope) AppInstanceARN() string { return s.tierFenced("arn:aws:rds:*:*:db:") }

func (s Scope) AppSubnetGroupARN() string { return s.tierFenced("arn:aws:rds:*:*:subgrp:") }

func (s Scope) AppClusterSnapshotARN() string {
	return s.tierFenced("arn:aws:rds:*:*:cluster-snapshot:")
}

func (s Scope) AppTaskDefinitionARN() string {
	return s.appFenced("arn:aws:ecs:*:*:task-definition/", ":*")
}

func (s Scope) ContainerTargetGroupARNs() []string {
	const targetGroups = "arn:aws:elasticloadbalancing:*:*:targetgroup/"
	return []string{s.appFenced(targetGroups, "/*"), targetGroups + awsports.PublicTargetGroupName(s.Namespace, s.Tier) + "/*"}
}

func (s Scope) AppQueueARN() string { return s.appFenced("arn:aws:sqs:*:*:", "") }

func (s Scope) AppTopicARN() string { return s.appFenced("arn:aws:sns:*:*:", "") }

func (s Scope) AppSubscriptionARN() string { return s.appFenced("arn:aws:sns:*:*:", ":*") }

func (s Scope) AppScheduleGroupARN() string {
	return s.appFenced("arn:aws:scheduler:*:*:schedule-group/", "")
}

func (s Scope) AppScheduleARN() string { return s.appFenced("arn:aws:scheduler:*:*:schedule/", "/*") }

func (s Scope) AppReplicationGroupARN() string {
	return s.appFenced("arn:aws:elasticache:*:*:replicationgroup:", "")
}

func (s Scope) AppCacheClusterARN() string {
	return s.appFenced("arn:aws:elasticache:*:*:cluster:", "")
}

func (s Scope) AppCacheParameterARN() string {
	return s.appFenced("arn:aws:elasticache:*:*:parametergroup:", "")
}

func (s Scope) AppCacheSubnetGroupARN() string {
	return s.appFenced("arn:aws:elasticache:*:*:subnetgroup:", "")
}

func (s Scope) AppAlarmARN() string { return s.appFenced("arn:aws:cloudwatch:*:*:alarm:", "") }

func (s Scope) containerCluster() string { return awsports.ContainerClusterName(s.Namespace, s.Tier) }

func (s Scope) publicBalancer() string { return awsports.PublicBalancerName(s.Namespace, s.Tier) }

func (s Scope) ContainerClusterARN() string { return "arn:aws:ecs:*:*:cluster/" + s.containerCluster() }

func (s Scope) ContainerServiceARN() string {
	return "arn:aws:ecs:*:*:service/" + s.containerCluster() + "/*"
}

func (s Scope) ContainerLogGroupARNs() []string {
	group := "arn:aws:logs:*:*:log-group:" + awsports.ContainerLogGroupName(s.Namespace, s.Tier)
	return []string{group, group + ":*"}
}

func (s Scope) ScalingAlarmARN() string {
	return "arn:aws:cloudwatch:*:*:alarm:TargetTracking-service/" + s.containerCluster() + "/*"
}

func (s Scope) balancers(arnPrefix, suffix string) []string {
	return []string{arnPrefix + s.containerCluster() + suffix, arnPrefix + s.publicBalancer() + suffix}
}

func (s Scope) ContainerBalancerARNs() []string {
	return s.balancers("arn:aws:elasticloadbalancing:*:*:loadbalancer/app/", "/*")
}

func (s Scope) ContainerListenerARNs() []string {
	return s.balancers("arn:aws:elasticloadbalancing:*:*:listener/app/", "/*/*")
}

func (s Scope) ContainerRuleARNs() []string {
	return s.balancers("arn:aws:elasticloadbalancing:*:*:listener-rule/app/", "/*/*/*")
}

func (s Scope) ContainerTrustStoreARN() string {
	return "arn:aws:elasticloadbalancing:*:*:truststore/" + s.publicBalancer() + "/*"
}

func (s Scope) TaggedOnlyAsItsTier() map[string]any {
	return map[string]any{"StringEqualsIfExists": map[string]any{
		"aws:ResourceTag/" + naming.EnvTierTagKey: string(s.Tier),
		"aws:RequestTag/" + naming.EnvTierTagKey:  string(s.Tier),
	}}
}

func (s Scope) RetaggedOnlyAsItsTier() map[string]any {
	return map[string]any{"StringEqualsIfExists": map[string]any{"aws:RequestTag/" + naming.EnvTierTagKey: string(s.Tier)}}
}

func (s Scope) TaggedOnCreateForItsTier() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:RequestTag/" + naming.EnvTierTagKey: string(s.Tier)}}
}

func (s Scope) TaggedForItsTier() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/" + naming.EnvTierTagKey: string(s.Tier)}}
}

func (s Scope) RetaggedOnlyWithinItsTier() map[string]any {
	return MergeConditions(s.TaggedForItsTier(), s.RetaggedOnlyAsItsTier())
}

func (s Scope) InvokesTheTagInvalidator() map[string]any {
	return map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": []string{s.TagInvalidatorFunction, s.TagInvalidatorFunction + ":*"}}}
}

func InCallerAccount() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceAccount": CallerAccount}}
}

func (s Scope) OfAppWorker() map[string]any {
	return map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": s.AppFunctionARN()}}
}

func KeyPairSuppliedByCaller() map[string]any {
	return map[string]any{"StringEqualsIfExists": map[string]any{"acm:CertificateKeyPairOrigin": "CUSTOMER_PROVIDED"}}
}

func (s Scope) ManagedByItsClusters() map[string]any {
	return map[string]any{"StringLike": map[string]any{"aws:ResourceTag/" + ManagedSecretClusterTagKey: s.AppClusterARN()}}
}

func (s Scope) UnevaluatedOrTaggedForItsTier() map[string]any {
	return map[string]any{"StringEqualsIfExists": map[string]any{"aws:ResourceTag/" + naming.EnvTierTagKey: string(s.Tier)}}
}

func (s Scope) TaggedOnCreateOnlyForItsTier() map[string]any {
	return MergeConditions(s.TaggedOnCreateForItsTier(), s.TaggedOnlyAsItsTier())
}

var serviceRolePolicyARNs = []string{LambdaBasicExecutionPolicyARN, LambdaVPCAccessPolicyARN, ECSTaskExecutionPolicyARN}

func AttachedPolicyIsAServiceRole() map[string]any {
	return map[string]any{
		"ArnEquals": map[string]any{"iam:PolicyARN": slices.Clone(serviceRolePolicyARNs)},
	}
}

func AttachedPolicyIsAServiceRoleOrInCallerAccount() map[string]any {
	return map[string]any{
		"StringLike": map[string]any{"iam:PolicyARN": append(slices.Clone(serviceRolePolicyARNs), CallerAccountPolicyARNs)},
	}
}

func IsAServiceRoleOrInAccount(policyARN, account string) bool {
	if slices.Contains(serviceRolePolicyARNs, policyARN) {
		return true
	}
	name, inAccount := strings.CutPrefix(policyARN, AccountPolicyARNPrefix(account))
	return inAccount && name != ""
}

func AccountPolicyARNPrefix(account string) string { return "arn:aws:iam::" + account + ":policy/" }

func PassedTo(service any) map[string]any {
	return map[string]any{"StringEquals": map[string]any{"iam:PassedToService": service}}
}

func ScalesECSServices(tagged map[string]any) map[string]any {
	return MergeConditions(tagged, map[string]any{"StringEquals": map[string]any{
		"application-autoscaling:service-namespace":  "ecs",
		"application-autoscaling:scalable-dimension": "ecs:service:DesiredCount",
	}})
}

func LinkedRoleFor(service string) map[string]any {
	return map[string]any{"StringEquals": map[string]any{"iam:AWSServiceName": service}}
}
