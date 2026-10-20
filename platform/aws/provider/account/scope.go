package account

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

var coreStackTables = []string{"StateTable", "VariablesTable"}

const anyBucketTag = "??????"

const functionARNPrefix = "arn:aws:lambda:*:*:function:"

func logGroupARNs(groups ...string) []string {
	var out []string
	for _, group := range groups {
		arn := "arn:aws:logs:*:*:log-group:" + group
		out = append(out, arn, arn+":*")
	}
	return out
}

func (n Namespace) Scope(tier environment.Tier) iam.Scope {
	core, _ := n.StackNameFor(tier)
	edgeUser, _ := n.EdgeUserNameFor(tier)
	origin, _ := n.OriginSecretParamFor(tier)
	sync := envsourcesync.NamesFor(string(n), tier)
	invalidator := taginvalidator.NamesFor(string(n), tier)
	optimizer := imageoptimizer.NamesFor(string(n), tier)
	s := iam.Scope{
		Namespace:         string(n),
		Tier:              tier,
		VariablesKeyAlias: n.variablesKeyAliasFor(tier),
		AppBoundary:       appBoundaryARNFor(n, tier),
		AppBoundaryPolicy: policyARN(n.AppBoundaryNameFor(tier)),
		AppRoles:          "arn:aws:iam::*:role" + n.AppRolePathFor(tier) + "*",
		BootstrapRoles:    []string{"arn:aws:iam::*:role" + n.bootstrapRolePathFor(tier) + "*", "arn:aws:iam::*:role/" + n.EdgeInvokeRoleName(tier)},
		PartRoles: []string{
			"arn:aws:iam::*:role" + n.AppRolePathFor(tier) + sync.Role,
			"arn:aws:iam::*:role" + n.AppRolePathFor(tier) + sync.ScheduleRole,
			"arn:aws:iam::*:role" + n.AppRolePathFor(tier) + optimizer.Role,
		},
		BastionRole: "arn:aws:iam::*:role/" + bastion.NameFor(string(n), tier),
		TierFunctions: []string{
			functionARNPrefix + sync.Function,
			functionARNPrefix + invalidator.Function,
			functionARNPrefix + optimizer.Function,
			functionARNPrefix + containers.LivenessName(string(n), tier),
		},
		TierFunctionLogGroups:      logGroupARNs(sync.LogGroup, invalidator.LogGroup, optimizer.LogGroup),
		EnvSourceSyncFunction:      functionARNPrefix + sync.Function,
		EnvSourceSyncScheduleGroup: "arn:aws:scheduler:*:*:schedule-group/" + sync.ScheduleGroup,
		EnvSourceSyncSchedule:      "arn:aws:scheduler:*:*:schedule/" + sync.ScheduleGroup + "/" + sync.Schedule,
		TagInvalidatorQueue:        "arn:aws:sqs:*:*:" + invalidator.Queue,
		TagInvalidatorRole:         "arn:aws:iam::*:role" + n.bootstrapRolePathFor(tier) + invalidator.Role,
		TagInvalidatorFunction:     functionARNPrefix + invalidator.Function,
		EdgeFunctions: []string{
			iam.EdgeFunctionARNPrefix + n.EdgeResolverName(tier),
			iam.EdgeFunctionARNPrefix + n.EdgeEmptyBodyName(tier),
		},
		EdgeUser:            "arn:aws:iam::*:user/" + edgeUser,
		PassphraseParameter: iam.ParameterARNPrefix + n.PassphraseParamFor(tier),
		OriginParameter:     iam.ParameterARNPrefix + origin,
		KVTokens:            iam.ParameterARNPrefix + n.KVTokenRoot() + "/" + naming.TierWord(tier) + "/*",
		SigningKeys:         "arn:aws:secretsmanager:*:*:secret:" + n.SigningKeyRoot() + "/" + naming.TierWord(tier) + "/*",
	}
	for _, class := range BootstrapStorageClasses {
		named := "arn:aws:s3:::" + n.BootstrapBucketName(tier, class, anyBucketTag)
		s.BootstrapBuckets = append(s.BootstrapBuckets, named)
		s.BootstrapObjects = append(s.BootstrapObjects, named+"/*")
	}
	for _, table := range coreStackTables {
		named := "arn:aws:dynamodb:*:*:table/" + core + "-" + table + "-*"
		s.BootstrapTables = append(s.BootstrapTables, named, named+"/*")
	}
	stacks := []string{core}
	for _, f := range partRegistry {
		stacks = append(stacks, f.stackName(n, tier))
	}
	for _, stack := range stacks {
		s.BootstrapStacks = append(s.BootstrapStacks, "arn:aws:cloudformation:*:*:stack/"+stack+"/*")
		s.BootstrapChangeSets = append(s.BootstrapChangeSets, "arn:aws:cloudformation:*:*:changeSet/"+stack+"-*/*")
	}
	for _, token := range slices.Sorted(maps.Values(runtimeArchTokens)) {
		layer := "arn:aws:lambda:*:*:layer:" + runtimeLayerPrefix(n, tier, token) + "*"
		s.RuntimeLayers = append(s.RuntimeLayers, layer)
		s.RuntimeLayerVersions = append(s.RuntimeLayerVersions, layer+":*")
	}
	for _, kind := range edgeKinds() {
		prefix, _ := n.EdgeParamPrefix(tier, kind)
		s.EdgeParameters = append(s.EdgeParameters, iam.ParameterARNPrefix+prefix+"/*")
	}
	return s
}
