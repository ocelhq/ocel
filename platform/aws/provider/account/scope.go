package account

import (
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

var (
	coreStackBuckets = []string{"StateBucket", "ArtifactBucket", "AssetBucket"}
	coreStackTables  = []string{"StateTable", "VariablesTable"}
)

func (n Namespace) Scope(tier environment.Tier) iam.Scope {
	core, _ := n.StackNameFor(tier)
	edgeUser, _ := n.EdgeUserNameFor(tier)
	origin, _ := n.OriginSecretParamFor(tier)
	sync := envsourcesync.NamesFor(string(n), tier)
	invalidator := taginvalidator.NamesFor(string(n), tier)
	s := iam.Scope{
		Tier:              tier,
		VariablesKeyAlias: n.variablesKeyAliasFor(tier),
		AppBoundary:       appBoundaryARNFor(n, tier),
		AppBoundaryPolicy: policyARN(n.AppBoundaryNameFor(tier)),
		AppRoles:          "arn:aws:iam::*:role" + n.AppRolePathFor(tier) + "*",
		BootstrapRoles:    []string{"arn:aws:iam::*:role" + n.bootstrapRolePathFor(tier) + "*", "arn:aws:iam::*:role/" + n.EdgeInvokeRoleName(tier)},
		PartRoles: []string{
			"arn:aws:iam::*:role" + n.AppRolePathFor(tier) + sync.Role,
			"arn:aws:iam::*:role" + n.AppRolePathFor(tier) + sync.ScheduleRole,
			"arn:aws:iam::*:role" + n.AppRolePathFor(tier) + imageoptimizer.NamesFor(string(n), tier).Role,
		},
		BastionRole:            "arn:aws:iam::*:role/" + bastion.NameFor(tier),
		EnvSourceSyncFunction:  "arn:aws:lambda:*:*:function:" + sync.Function,
		TagInvalidatorRole:     "arn:aws:iam::*:role" + n.bootstrapRolePathFor(tier) + invalidator.Role,
		TagInvalidatorFunction: "arn:aws:lambda:*:*:function:" + invalidator.Function,
		EdgeFunctions: []string{
			iam.EdgeFunctionARNPrefix + n.EdgeResolverName(tier),
			iam.EdgeFunctionARNPrefix + n.EdgeEmptyBodyName(tier),
		},
		EdgeUser:            "arn:aws:iam::*:user/" + edgeUser,
		PassphraseParameter: iam.ParameterARNPrefix + n.PassphraseParamFor(tier),
		OriginParameter:     iam.ParameterARNPrefix + origin,
		KVTokens:            iam.ParameterARNPrefix + n.KVTokenRoot() + "/*",
		SigningKeys:         "arn:aws:secretsmanager:*:*:secret:" + n.SigningKeyRoot() + "/*",
	}
	for _, bucket := range coreStackBuckets {
		named := "arn:aws:s3:::" + strings.ToLower(core+"-"+bucket) + "-*"
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
