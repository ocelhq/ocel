package iam

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

type Scope struct {
	Tier environment.Tier

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

	RuntimeLayers        []string
	RuntimeLayerVersions []string

	BastionRole            string
	EnvSourceSyncFunction  string
	TagInvalidatorRole     string
	TagInvalidatorFunction string
	EdgeFunctions          []string
	EdgeUser               string

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

func (s Scope) MadeByItsStacks() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/" + naming.EnvTierTagKey: string(s.Tier)}}
}

func (s Scope) MadeForItsTier() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:RequestTag/" + naming.EnvTierTagKey: string(s.Tier)}}
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
	return MergeConditions(TaggedOnCreate(), s.MadeForItsTier())
}

func (s Scope) TaggedByOcelForItsTier() map[string]any {
	return MergeConditions(TaggedByOcel(), s.MadeByItsStacks())
}

func (s Scope) RetaggedByOcelOnlyAsItsTier() map[string]any {
	return MergeConditions(s.TaggedByOcelForItsTier(), s.RetaggedOnlyAsItsTier())
}

func (s Scope) InvokesTheTagInvalidator() map[string]any {
	return map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": []string{s.TagInvalidatorFunction, s.TagInvalidatorFunction + ":*"}}}
}

func InCallerAccount() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceAccount": CallerAccount}}
}

func TaggedOnCreate() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:RequestTag/" + ManagedByTagKey: ManagedByTagValue}}
}

func TaggedByOcel() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/" + ManagedByTagKey: ManagedByTagValue}}
}

func OfAppWorker() map[string]any {
	return map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": AppWorkerFunctionARN}}
}

func KeyPairSuppliedByCaller() map[string]any {
	return map[string]any{"StringEqualsIfExists": map[string]any{"acm:CertificateKeyPairOrigin": "CUSTOMER_PROVIDED"}}
}

func TierScopePrefix(tier environment.Tier) string { return AppScopePrefix + string(tier) + "-" }

func (s Scope) Buckets() string { return "arn:aws:s3:::" + TierScopePrefix(s.Tier) + "*" }

func (s Scope) Clusters() string { return "arn:aws:rds:*:*:cluster:" + TierScopePrefix(s.Tier) + "*" }

func (s Scope) ClusterSnapshots() string {
	return "arn:aws:rds:*:*:cluster-snapshot:" + TierScopePrefix(s.Tier) + "*"
}

func (s Scope) ManagedByItsClusters() map[string]any {
	return map[string]any{"StringLike": map[string]any{"aws:ResourceTag/" + ManagedSecretClusterTagKey: s.Clusters()}}
}

func (s Scope) TaggedOnCreateOnlyForItsTier() map[string]any {
	return MergeConditions(s.TaggedOnCreateForItsTier(), s.TaggedOnlyAsItsTier())
}

func AttachedPolicyIsAServiceRole(resourceTagged bool) map[string]any {
	condition := map[string]any{
		"ArnEquals": map[string]any{"iam:PolicyARN": []string{LambdaBasicExecutionPolicyARN, LambdaVPCAccessPolicyARN, ECSTaskExecutionPolicyARN}},
	}
	if resourceTagged {
		return MergeConditions(condition, TaggedByOcel())
	}
	return condition
}

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
