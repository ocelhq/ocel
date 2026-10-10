package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

type parsedPolicy struct {
	Version   string            `json:"Version"`
	Statement []parsedStatement `json:"Statement"`
}

type parsedStatement struct {
	Effect    string          `json:"Effect"`
	Action    json.RawMessage `json:"Action"`
	Resource  json.RawMessage `json:"Resource"`
	Condition map[string]any  `json:"Condition"`
}

type grant struct {
	action    string
	resource  string
	condition string
}

var scopingConditionKeys = []string{
	"aws:RequestTag/ocel:",
	"aws:ResourceTag/ocel:",
	"ec2:CreateAction",
	"iam:PassedToService",
	"iam:PermissionsBoundary",
	"iam:PolicyARN",
	"kms:ResourceAliases",
	"lambda:FunctionArn",
}

var actionsNoTagScopes = []string{
	"iam:AttachRolePolicy",
	"iam:CreateRole",
	"iam:PutRolePermissionsBoundary",
	"iam:PutRolePolicy",
	"iam:UpdateAssumeRolePolicy",
}

var actionsAWSGivesNoScopingKey = []string{
	"ecs:DeregisterTaskDefinition",
}

var bootstrapOnlyActions = []string{
	"cloudformation:DeleteStack",
	"dynamodb:CreateTable",
	"dynamodb:DeleteTable",
	"iam:CreateAccessKey",
	"iam:CreatePolicy",
	"iam:CreateUser",
	"iam:DeletePolicy",
	"iam:DeleteAccessKey",
	"iam:DeleteRolePermissionsBoundary",
	"iam:DeleteUser",
	"iam:ListAccessKeys",
	"iam:PutUserPolicy",
	"kms:CreateAlias",
	"kms:CreateKey",
	"kms:PutKeyPolicy",
	"kms:ScheduleKeyDeletion",
}

func parsePolicy(t *testing.T, document string) parsedPolicy {
	t.Helper()
	var policy parsedPolicy
	if err := json.Unmarshal([]byte(document), &policy); err != nil {
		t.Fatalf("parse policy: %v\n%s", err, document)
	}
	if policy.Version != "2012-10-17" {
		t.Fatalf("policy Version = %q, want 2012-10-17", policy.Version)
	}
	if len(policy.Statement) == 0 {
		t.Fatal("policy has no statement")
	}
	return policy
}

func stringsOf(t *testing.T, raw json.RawMessage, field string) []string {
	t.Helper()
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		t.Fatalf("parse %s %s: %v", field, raw, err)
	}
	return many
}

func grantsOf(t *testing.T, document string) map[grant]bool {
	t.Helper()
	grants := map[grant]bool{}
	for _, statement := range parsePolicy(t, document).Statement {
		if statement.Effect != "Allow" {
			t.Fatalf("statement Effect = %q, want Allow", statement.Effect)
		}
		condition, err := json.Marshal(statement.Condition)
		if err != nil {
			t.Fatalf("marshal condition: %v", err)
		}
		for _, action := range stringsOf(t, statement.Action, "Action") {
			for _, resource := range stringsOf(t, statement.Resource, "Resource") {
				grants[grant{action: action, resource: resource, condition: string(condition)}] = true
			}
		}
	}
	return grants
}

func actionsOf(t *testing.T, document string) map[string]bool {
	t.Helper()
	actions := map[string]bool{}
	for g := range grantsOf(t, document) {
		actions[g.action] = true
	}
	return actions
}

func renderedCredentials(t *testing.T) (string, string) {
	t.Helper()
	return renderedCredentialsOf(t, environment.TierProduction)
}

func renderedCredentialsOf(t *testing.T, tier environment.Tier) (string, string) {
	t.Helper()
	bootstrapDoc, err := BootstrapCredentialPermissions(defaultNamespace, tier, "")
	if err != nil {
		t.Fatalf("BootstrapCredentialPermissions(defaultNamespace, %s, \"\") error = %v", tier, err)
	}
	deployDoc, err := DeployCredentialPermissions(defaultNamespace, tier, "")
	if err != nil {
		t.Fatalf("DeployCredentialPermissions(defaultNamespace, %s, \"\") error = %v", tier, err)
	}
	return bootstrapDoc, deployDoc
}

func bothCredentials(t *testing.T) map[string]string {
	t.Helper()
	credentials := map[string]string{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		credentials["bootstrap "+string(tier)] = bootstrapDoc
		credentials["deploy "+string(tier)] = deployDoc
	}
	return credentials
}

func readOnly(action string) bool {
	_, verb, ok := strings.Cut(action, ":")
	if !ok {
		return false
	}
	for _, prefix := range []string{"Describe", "Get", "List"} {
		if strings.HasPrefix(verb, prefix) {
			return true
		}
	}
	return false
}

var servicesWhoseARNsNameTheResourceDirectly = []string{"s3", "sns", "sqs"}

func namesSomething(resource string) bool {
	fields := strings.SplitN(resource, ":", 6)
	if len(fields) < 6 {
		return false
	}
	parts := strings.FieldsFunc(fields[5], func(r rune) bool { return r == '/' || r == ':' })
	at := 1
	if slices.Contains(servicesWhoseARNsNameTheResourceDirectly, fields[2]) {
		at = 0
	}
	return len(parts) > at && parts[at] != "*"
}

func boundaryScopes(condition map[string]any) bool {
	operands, ok := condition["StringEquals"].(map[string]any)
	if !ok {
		return false
	}
	named, ok := operands["iam:PermissionsBoundary"].(string)
	if !ok {
		return false
	}
	return slices.Contains([]string{appBoundaryARNFor(defaultNamespace, environment.TierProduction), appBoundaryARNFor(defaultNamespace, environment.TierPreview)}, named)
}

func conditionScopes(actions []string, condition map[string]any) bool {
	if slices.ContainsFunc(actions, func(a string) bool { return slices.Contains(actionsNoTagScopes, a) }) {
		return boundaryScopes(condition)
	}
	return taggedOrNamedScopes(condition)
}

func taggedOrNamedScopes(condition map[string]any) bool {
	for _, operands := range condition {
		keyed, ok := operands.(map[string]any)
		if !ok {
			continue
		}
		for key := range keyed {
			for _, scoping := range scopingConditionKeys {
				if strings.HasPrefix(key, scoping) {
					return true
				}
			}
		}
	}
	return false
}

func TestNoCredentialMintsARoleThatCanOutgrowItsBoundary(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		for _, statement := range parsePolicy(t, document).Statement {
			actions := stringsOf(t, statement.Action, "Action")
			for _, action := range actions {
				if !slices.Contains(actionsNoTagScopes, action) {
					continue
				}
				for _, resource := range stringsOf(t, statement.Resource, "Resource") {
					if slices.Contains(defaultNamespace.ScopedARNs(environment.TierProduction).bootstrapRoles, resource) || slices.Contains(defaultNamespace.ScopedARNs(environment.TierPreview).bootstrapRoles, resource) {
						continue
					}
					if !boundaryScopes(statement.Condition) {
						t.Errorf(
							"the %s credential grants %s on %q without pinning iam:PermissionsBoundary, so it can mint a role that reaches further than the credential itself",
							purpose, action, resource,
						)
					}
				}
			}
		}
	}
}

func TestNoCredentialRepointsTheTrustPolicyOfAnAppRole(t *testing.T) {
	deployActions := actionsOf(t, mustRender(t, DeployCredentialPermissions))
	if deployActions["iam:UpdateAssumeRolePolicy"] {
		t.Error("the deploy credential grants iam:UpdateAssumeRolePolicy, which hands an app role's trust policy to whoever has the credential")
	}
}

func TestTheBootstrapCredentialIsAStrictSupersetOfTheDeployCredential(t *testing.T) {
	bootstrapDoc, deployDoc := renderedCredentials(t)
	bootstrapGrants, deployGrants := grantsOf(t, bootstrapDoc), grantsOf(t, deployDoc)

	var missing []string
	for g := range deployGrants {
		if !bootstrapGrants[g] {
			missing = append(missing, g.action+" on "+g.resource)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("the deploy credential grants what the bootstrap credential does not: %s", strings.Join(missing, ", "))
	}

	extra := 0
	for g := range bootstrapGrants {
		if !deployGrants[g] {
			extra++
		}
	}
	if extra == 0 {
		t.Error("the bootstrap credential grants nothing the deploy credential lacks, so the two are the same credential")
	}
}

func TestTheDeployCredentialWithholdsWhatDefinesTheBootstrapCredential(t *testing.T) {
	bootstrapDoc, deployDoc := renderedCredentials(t)
	bootstrapActions, deployActions := actionsOf(t, bootstrapDoc), actionsOf(t, deployDoc)

	for _, action := range bootstrapOnlyActions {
		if deployActions[action] {
			t.Errorf("the deploy credential grants %s, which is what a bootstrap credential is for", action)
		}
		if !bootstrapActions[action] {
			t.Errorf("the bootstrap credential no longer grants %s, so no Ocel credential may call %s at all", action, action)
		}
	}
}

func TestTheDeployCredentialPublishesTheRuntimeStackAndNoOtherStack(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	r := defaultNamespace.ScopedARNs(environment.TierProduction)
	unconditional := conditionJSON(t, nil)

	for _, action := range []string{
		"cloudformation:CreateChangeSet",
		"cloudformation:CreateStack",
		"cloudformation:DescribeStackEvents",
	} {
		if !grants[grant{action: action, resource: r.runtimeStack, condition: unconditional}] {
			t.Errorf("the deploy credential does not grant %s on %s, so a deploy onto an account an older build bootstrapped cannot publish the runtime its functions boot through", action, r.runtimeStack)
		}
	}
	for _, action := range []string{
		"cloudformation:DeleteChangeSet",
		"cloudformation:DescribeChangeSet",
		"cloudformation:ExecuteChangeSet",
	} {
		for _, resource := range []string{r.runtimeStack, r.runtimeChangeSet} {
			if !grants[grant{action: action, resource: resource, condition: unconditional}] {
				t.Errorf("the deploy credential does not grant %s on %s, so the runtime it publishes is planned and never executed", action, resource)
			}
		}
	}

	for g := range grants {
		if !strings.HasPrefix(g.action, "cloudformation:") || readOnly(g.action) {
			continue
		}
		if g.resource != r.runtimeStack && g.resource != r.runtimeChangeSet {
			t.Errorf("the deploy credential grants %s on %s, which is a bootstrap stack a deploy never writes", g.action, g.resource)
		}
	}
}

func TestTheDeployCredentialOwnsTheLogGroupsItCreates(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	want := map[string]string{
		"logs:CreateLogGroup":      conditionJSON(t, taggedOnCreate()),
		"logs:DeleteLogGroup":      conditionJSON(t, taggedByOcel()),
		"logs:FilterLogEvents":     conditionJSON(t, taggedByOcel()),
		"logs:ListTagsForResource": conditionJSON(t, taggedByOcel()),
		"logs:PutRetentionPolicy":  conditionJSON(t, taggedByOcel()),
		"logs:StartLiveTail":       conditionJSON(t, taggedByOcel()),
		"logs:TagResource":         conditionJSON(t, taggedByOcel()),
		"logs:UntagResource":       conditionJSON(t, taggedByOcel()),
	}
	for _, resource := range []string{appLogGroupARN, functionLogGroupARN} {
		got := logsGrantsOn(grants, resource)
		if !maps.Equal(got, want) {
			t.Errorf("the deploy credential grants %v on %s, want exactly %v, so a log group is either never made with a retention, never reclaimed by the teardown, or reachable beyond what ocel tagged", got, resource, want)
		}
	}
}

func TestTheDeployCredentialIsGrantedGetFunctionConfiguration(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	for g := range grants {
		if g.action == "lambda:GetFunctionConfiguration" {
			return
		}
	}
	t.Error("the deploy credential does not grant lambda:GetFunctionConfiguration, so a log read cannot learn which log group a function writes to")
}

func TestEveryCredentialListsLogGroupsOnTheOnlyResourceAWSAccepts(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		if !grants[grant{action: "logs:DescribeLogGroups", resource: UnscopedResource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the %s credential does not grant logs:DescribeLogGroups on %q, the only resource IAM evaluates it against, so CloudFormation cannot read back a log group it manages", purpose, UnscopedResource)
		}
		for g := range grants {
			if g.action == "logs:DescribeLogGroups" && g.resource != UnscopedResource {
				t.Errorf("the %s credential grants logs:DescribeLogGroups on %s, an ARN IAM never matches for an action with no resource type", purpose, g.resource)
			}
		}
	}
}

func TestEveryCredentialScopesTaskDefinitionsToWhatAWSEvaluates(t *testing.T) {
	unscopable := []string{"ecs:DeregisterTaskDefinition", "ecs:DescribeTaskDefinition", "ecs:ListTaskDefinitions"}
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		if !grants[grant{action: "ecs:RegisterTaskDefinition", resource: appTaskDefinitionARN, condition: conditionJSON(t, taggedOnCreate())}] {
			t.Errorf("the %s credential does not grant ecs:RegisterTaskDefinition on %s under a create tag, so a container release either cannot register its family or registers one nothing marks as Ocel's", purpose, appTaskDefinitionARN)
		}
		for _, action := range unscopable {
			if !grants[grant{action: action, resource: UnscopedResource, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential does not grant %s on %q, the only resource IAM evaluates it against, so a container release cannot read back or reclaim its task definition", purpose, action, UnscopedResource)
			}
		}
		for g := range grants {
			if g.action == "ecs:RegisterTaskDefinition" && g.resource != appTaskDefinitionARN {
				t.Errorf("the %s credential grants ecs:RegisterTaskDefinition on %s, which reaches past the task definitions a deploy registers", purpose, g.resource)
			}
			if slices.Contains(unscopable, g.action) && g.resource != UnscopedResource {
				t.Errorf("the %s credential grants %s on %s, an ARN IAM never matches for an action with no resource type", purpose, g.action, g.resource)
			}
		}
	}
}

func logsGrantsOn(grants map[grant]bool, resource string) map[string]string {
	got := map[string]string{}
	for g := range grants {
		if g.resource == resource && strings.HasPrefix(g.action, "logs:") {
			got[g.action] = g.condition
		}
	}
	return got
}

func conditionJSON(t *testing.T, condition map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(condition)
	if err != nil {
		t.Fatalf("marshal condition: %v", err)
	}
	return string(encoded)
}

func TestOnlyTheEdgeUserIsMintedAndItHasNoManagedPolicy(t *testing.T) {
	for _, tier := range bothTiers {
		for purpose, document := range credentialsOfTier(t, tier) {
			for g := range grantsOf(t, document) {
				if g.action == "iam:AttachUserPolicy" {
					t.Errorf("the %s %s credential grants iam:AttachUserPolicy, which turns a minted user into whatever policy it names", tier, purpose)
				}
				if !strings.HasPrefix(g.action, "iam:") || !strings.Contains(g.action, "User") && !strings.Contains(g.action, "AccessKey") {
					continue
				}
				if g.resource != defaultNamespace.ScopedARNs(tier).edgeUser {
					t.Errorf("the %s %s credential grants %s on %q, which is not its tier's edge user", tier, purpose, g.action, g.resource)
				}
			}
		}
	}

	bootstrapActions := actionsOf(t, mustRender(t, BootstrapCredentialPermissions))
	for _, action := range []string{
		"iam:CreateUser",
		"iam:PutUserPolicy",
		"iam:CreateAccessKey",
		"iam:ListAccessKeys",
		"iam:DeleteAccessKey",
	} {
		if !bootstrapActions[action] {
			t.Errorf("the bootstrap credential does not grant %s, which minting the edge user needs", action)
		}
	}
}

func TestCredentialsNameActionsRatherThanGlobbingThem(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			service, verb, ok := strings.Cut(g.action, ":")
			if !ok || service == "" || strings.Contains(service, "*") {
				t.Errorf("the %s credential grants %q, which names no service", purpose, g.action)
				continue
			}
			if verb == "" || strings.HasPrefix(verb, "*") {
				t.Errorf("the %s credential grants %q, whose leading wildcard matches verbs nobody enumerated", purpose, g.action)
			}
		}
	}
}

func TestEveryMutatingGrantHasAnOcelScope(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		for _, statement := range parsePolicy(t, document).Statement {
			actions := stringsOf(t, statement.Action, "Action")
			mutating := slices.DeleteFunc(slices.Clone(actions), func(action string) bool {
				return readOnly(action) || slices.Contains(actionsAWSGivesNoScopingKey, action)
			})
			if len(mutating) == 0 {
				continue
			}
			if conditionScopes(actions, statement.Condition) {
				continue
			}
			for _, resource := range stringsOf(t, statement.Resource, "Resource") {
				if !namesSomething(resource) {
					t.Errorf(
						"the %s credential grants %s on %q, which names nothing Ocel owns and has no scoping condition",
						purpose, strings.Join(mutating, ", "), resource,
					)
				}
			}
		}
	}
}

func TestNoCredentialTagsAKeyItDoesNotAlreadyOwn(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		for _, statement := range parsePolicy(t, document).Statement {
			actions := stringsOf(t, statement.Action, "Action")
			tagging := slices.DeleteFunc(slices.Clone(actions), func(action string) bool {
				return action != "kms:TagResource" && action != "kms:UntagResource"
			})
			if len(tagging) == 0 {
				continue
			}
			if !conditionNames(statement.Condition, "aws:ResourceTag/"+VariablesKeyComponentTagKey) {
				t.Errorf(
					"the %s credential grants %s on %s with no aws:ResourceTag condition, so it may tag a key ocel never made and then open what that key seals",
					purpose, strings.Join(tagging, ", "), strings.Join(stringsOf(t, statement.Resource, "Resource"), ", "),
				)
			}
		}
	}
}

func conditionNames(condition map[string]any, key string) bool {
	for _, operands := range condition {
		keyed, ok := operands.(map[string]any)
		if !ok {
			continue
		}
		if _, named := keyed[key]; named {
			return true
		}
	}
	return false
}

func mustRender(t *testing.T, render func(Namespace, environment.Tier, string) (string, error)) string {
	t.Helper()
	document, err := render(defaultNamespace, environment.TierProduction, "")
	if err != nil {
		t.Fatalf("render policy: %v", err)
	}
	return document
}

func TestTheBootstrapCredentialOwnsOnlyTheLogGroupsItsStacksDeclare(t *testing.T) {
	bootstrapDoc, deployDoc := renderedCredentials(t)
	bootstrapGrants, deployGrants := grantsOf(t, bootstrapDoc), grantsOf(t, deployDoc)
	scopes := defaultNamespace.ScopedARNs(environment.TierProduction).bootstrapLogGroups
	unconditional := conditionJSON(t, nil)
	want := map[string]string{
		"logs:CreateLogGroup":      unconditional,
		"logs:DeleteLogGroup":      unconditional,
		"logs:ListTagsForResource": unconditional,
		"logs:PutRetentionPolicy":  unconditional,
		"logs:TagResource":         unconditional,
		"logs:UntagResource":       unconditional,
	}
	for _, f := range featureRegistry {
		scope := "arn:aws:logs:*:*:log-group:/aws/lambda/" + f.stackName(defaultNamespace, environment.TierProduction) + "-*"
		if got := logsGrantsOn(bootstrapGrants, scope); !maps.Equal(got, want) {
			t.Errorf("the bootstrap credential grants %v on %s, want exactly %v, so a bootstrap stack either cannot create, bound and reclaim its functions' log groups or reaches beyond them", got, scope, want)
		}
	}
	for _, resource := range []string{appLogGroupARN, functionLogGroupARN} {
		if got, deploy := logsGrantsOn(bootstrapGrants, resource), logsGrantsOn(deployGrants, resource); !maps.Equal(got, deploy) {
			t.Errorf("the bootstrap credential grants %v on %s, want the deploy credential's %v, so bootstrapping widens what a deploy may do to a log group", got, resource, deploy)
		}
	}
	for g := range bootstrapGrants {
		if !strings.HasPrefix(g.action, "logs:") {
			continue
		}
		switch {
		case slices.Contains(scopes, g.resource), g.resource == appLogGroupARN, g.resource == functionLogGroupARN:
		case g.resource == UnscopedResource:
			if g.action != "logs:DescribeLogGroups" {
				t.Errorf("the bootstrap credential grants %s on %q, which reaches every log group in the account", g.action, g.resource)
			}
		default:
			t.Errorf("the bootstrap credential grants %s on %s, beyond the log groups a bootstrap or a deploy owns", g.action, g.resource)
		}
	}
}

func TestEveryCredentialReachesOnlyTheBucketsAndClustersDeploysNameUnderTheAppScope(t *testing.T) {
	var bootstrapStores []string
	for _, tier := range bothTiers {
		bootstrapARNs := defaultNamespace.ScopedARNs(tier)
		bootstrapStores = slices.Concat(bootstrapStores, bootstrapARNs.bootstrapBuckets, bootstrapARNs.bootstrapObjects)
	}
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			switch {
			case strings.HasPrefix(g.action, "s3:"):
				if slices.Contains(bootstrapStores, g.resource) {
					continue
				}
				if !strings.HasPrefix(g.resource, "arn:aws:s3:::"+appScopePrefix) {
					t.Errorf("the %s credential grants %s on %s, a bucket name a deploy never creates: S3 evaluates no Ocel tag on a bucket, so the name prefix is the only scope", purpose, g.action, g.resource)
				}
			case strings.HasPrefix(g.action, "rds:") && !readOnly(g.action):
				for _, kind := range []string{"cluster:", "db:", "subgrp:"} {
					if strings.Contains(g.resource, ":"+kind) && !strings.Contains(g.resource, ":"+kind+appScopePrefix) {
						t.Errorf("the %s credential grants %s on %s, an identifier a deploy never mints", purpose, g.action, g.resource)
					}
				}
			case strings.HasPrefix(g.action, "secretsmanager:") && g.resource == appSecretARN:
				if g.condition != conditionJSON(t, managedByAnAppCluster()) {
					t.Errorf("the %s credential grants %s on %s under %s, want the secret pinned to a cluster in the app scope through the tag RDS stamps on it, or the credential reads every Aurora master password in the account", purpose, g.action, g.resource, g.condition)
				}
			}
		}
	}
}

func TestEveryCredentialProvisionsKVStoresOnlyUnderTheAppScope(t *testing.T) {
	group, member := "arn:aws:elasticache:*:*:replicationgroup:ocel-app-*", "arn:aws:elasticache:*:*:cluster:ocel-app-*"
	parameters, subnets := "arn:aws:elasticache:*:*:parametergroup:ocel-app-*", "arn:aws:elasticache:*:*:subnetgroup:ocel-app-*"
	on := map[string][]string{
		"elasticache:CreateReplicationGroup":       {group, member, parameters, subnets},
		"elasticache:ModifyReplicationGroup":       {group, parameters},
		"elasticache:DeleteReplicationGroup":       {group},
		"elasticache:DescribeReplicationGroups":    {group},
		"elasticache:IncreaseReplicaCount":         {group},
		"elasticache:DecreaseReplicaCount":         {group},
		"elasticache:DescribeCacheClusters":        {"arn:aws:elasticache:*:*:cluster:*"},
		"elasticache:CreateCacheParameterGroup":    {parameters},
		"elasticache:ModifyCacheParameterGroup":    {parameters},
		"elasticache:DeleteCacheParameterGroup":    {parameters},
		"elasticache:DescribeCacheParameterGroups": {parameters},
		"elasticache:DescribeCacheParameters":      {parameters},
		"elasticache:CreateCacheSubnetGroup":       {subnets},
		"elasticache:ModifyCacheSubnetGroup":       {subnets},
		"elasticache:DeleteCacheSubnetGroup":       {subnets},
		"elasticache:DescribeCacheSubnetGroups":    {subnets},
		"elasticache:AddTagsToResource":            {group, member, parameters, subnets},
		"elasticache:ListTagsForResource":          {group, member, parameters, subnets},
		"elasticache:RemoveTagsFromResource":       {group, member, parameters, subnets},
	}
	want := map[grant]bool{}
	for action, resources := range on {
		for _, resource := range resources {
			want[grant{action: action, resource: resource, condition: conditionJSON(t, nil)}] = true
		}
	}
	for purpose, document := range bothCredentials(t) {
		granted := map[grant]bool{}
		for g := range grantsOf(t, document) {
			if strings.HasPrefix(g.action, "elasticache:") {
				granted[g] = true
			}
		}
		if !maps.Equal(granted, want) {
			t.Errorf("the %s credential grants elasticache %v, want exactly %v: a store's replication group, members, parameter group and subnet group, each named under the app scope", purpose, granted, want)
		}
	}
}

func TestEveryCredentialMayCreateElastiCachesLinkedRole(t *testing.T) {
	want := grant{
		action:    "iam:CreateServiceLinkedRole",
		resource:  "arn:aws:iam::*:role/aws-service-role/elasticache.amazonaws.com/*",
		condition: conditionJSON(t, linkedRoleFor("elasticache.amazonaws.com")),
	}
	for purpose, document := range bothCredentials(t) {
		if !grantsOf(t, document)[want] {
			t.Errorf("the %s credential cannot create ElastiCache's service-linked role, which the first store in an account needs to place its nodes in the VPC", purpose)
		}
	}
}

func TestEveryCredentialReadsWritesAndDeletesTheKVTokensAndNoOtherParameterThatWay(t *testing.T) {
	tokens := parameterARNPrefix + defaultNamespace.KVTokenRoot() + "/*"
	for purpose, document := range bothCredentials(t) {
		granted := grantsOf(t, document)
		for _, action := range []string{"ssm:GetParameter", "ssm:GetParametersByPath", "ssm:PutParameter", "ssm:DeleteParameter"} {
			if !granted[grant{action: action, resource: tokens, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential does not grant %s on %s, where a deploy keeps each store's AUTH token", purpose, action, tokens)
			}
		}
	}
}

func TestEveryCredentialTouchesOnlyEventSourceMappingsOfTheBootstrapsFunctionsOrOcelTaggedOnesOfAppWorkers(t *testing.T) {
	ofAppWorkers := map[string]any{"lambda:FunctionArn": "arn:aws:lambda:*:*:function:ocel-app-*"}
	for _, tier := range bothTiers {
		r := defaultNamespace.ScopedARNs(tier)
		allowed := map[string]bool{
			conditionJSON(t, map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": r.bootstrapFunction}}): true,
			conditionJSON(t, map[string]any{
				"StringEquals": map[string]any{"aws:RequestTag/ocel:managed-by": "ocel", "aws:RequestTag/ocel:env-tier": string(tier)},
				"ArnLike":      ofAppWorkers,
			}): true,
			conditionJSON(t, map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/ocel:managed-by": "ocel", "aws:ResourceTag/ocel:env-tier": string(tier)},
				"ArnLike":      ofAppWorkers,
			}): true,
		}
		for purpose, document := range credentialsOfTier(t, tier) {
			for g := range grantsOf(t, document) {
				if !strings.HasSuffix(g.action, "EventSourceMapping") {
					continue
				}
				if !allowed[g.condition] {
					t.Errorf("the %s %s credential grants %s on %s under %s, want it pinned to the bootstrap's own functions or to its own tier's Ocel-tagged mappings of app worker functions", tier, purpose, g.action, g.resource, g.condition)
				}
			}
		}
	}
}

func TestTheDeployCredentialMakesAndRemovesOnlyTheQueuesTopicsAndSchedulesOfApps(t *testing.T) {
	scoped := map[string][]string{
		"sqs:CreateQueue":               {appQueueARN},
		"sqs:DeleteQueue":               {appQueueARN},
		"sns:CreateTopic":               {appTopicARN, appSubscriptionARN},
		"sns:DeleteTopic":               {appTopicARN, appSubscriptionARN},
		"scheduler:CreateSchedule":      {appScheduleARN},
		"scheduler:DeleteSchedule":      {appScheduleARN},
		"scheduler:CreateScheduleGroup": {appScheduleGroupARN},
		"scheduler:DeleteScheduleGroup": {appScheduleGroupARN},
	}
	granted := map[string]bool{}
	for g := range grantsOf(t, mustRender(t, DeployCredentialPermissions)) {
		resources, watched := scoped[g.action]
		if !watched {
			continue
		}
		granted[g.action] = true
		if !slices.Contains(resources, g.resource) {
			t.Errorf("the deploy credential grants %s on %s, want it only on %v, the app scope a deploy names its queues, topics and schedules under", g.action, g.resource, resources)
		}
	}
	for action := range scoped {
		if !granted[action] {
			t.Errorf("the deploy credential grants no %s, and a deploy of a task, topic or cron task needs it", action)
		}
	}
}

func TestOnlyTheBootstrapCredentialDeletesThePulumiPassphraseAndOnlyByItsExactPath(t *testing.T) {
	r := defaultNamespace.ScopedARNs(environment.TierProduction)
	if _, path, _ := strings.Cut(r.passphraseParam, ":parameter"); strings.ContainsAny(path, "*?") {
		t.Fatalf("the passphrase ARN %q names a parameter pattern, and the one delete grant on it must name the path exactly", r.passphraseParam)
	}
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			if !strings.HasPrefix(g.action, "ssm:Delete") || !iamResourceMatches(g.resource, r.passphraseParam) {
				continue
			}
			if !strings.HasPrefix(purpose, "bootstrap") {
				t.Errorf("the %s credential grants %s on %s, which reaches %s: only the credential that can already destroy every Pulumi state in the account may take what encrypts it", purpose, g.action, g.resource, r.passphraseParam)
				continue
			}
			if g.action != "ssm:DeleteParameter" || g.resource != r.passphraseParam || g.condition != conditionJSON(t, nil) {
				t.Errorf("the bootstrap credential grants %s on %s under %s, want ssm:DeleteParameter on exactly %s: a wider grant reaches the passphrase by accident from a tree it was meant to prune", g.action, g.resource, g.condition, r.passphraseParam)
			}
		}
	}
	bootstrapGrants := grantsOf(t, mustRender(t, BootstrapCredentialPermissions))
	for _, resource := range slices.Concat([]string{r.originParam, r.passphraseParam}, r.edgeParams) {
		if !bootstrapGrants[grant{action: "ssm:DeleteParameter", resource: resource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the bootstrap credential cannot delete %s, which a teardown reclaims", resource)
		}
	}
}

func TestABootstrapCredentialWritesAndDeletesThePassphraseOfOnlyItsOwnTier(t *testing.T) {
	const account = "arn:aws:ssm:us-east-1:111122223333:parameter"
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		own := account + defaultNamespace.PassphraseParamFor(tier)
		other := account + defaultNamespace.PassphraseParamFor(sibling)
		for _, action := range []string{"ssm:GetParameter", "ssm:PutParameter", "ssm:DeleteParameter"} {
			if !allows(t, bootstrapDoc, action, own, nil) {
				t.Errorf("the %s bootstrap credential cannot %s on %s, its own tier's passphrase", tier, action, own)
			}
			if allows(t, bootstrapDoc, action, other, nil) {
				t.Errorf("the %s bootstrap credential can %s on %s, the passphrase the %s tier's Pulumi state is encrypted under", tier, action, other, sibling)
			}
		}
	}
}

func TestEveryCredentialMayGrantLambdaTheVariablesKeyOfItsTierAndNoOther(t *testing.T) {
	for _, tier := range bothTiers {
		want := conditionJSON(t, mergeConditions(defaultNamespace.ScopedARNs(tier).variablesKey(), map[string]any{
			"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
			"Bool":         map[string]any{"kms:GrantIsForAWSResource": "true"},
		}))
		for purpose, document := range credentialsOfTier(t, tier) {
			grants := grantsOf(t, document)
			if !grants[grant{action: "kms:CreateGrant", resource: AnyKeyARN, condition: want}] {
				t.Errorf("the %s %s credential does not grant kms:CreateGrant on its tier's variables key for an AWS service, so Lambda cannot seal a function's environment under it", tier, purpose)
			}
			for g := range grants {
				if g.action == "kms:CreateGrant" && g.condition != want {
					t.Errorf("the %s %s credential grants kms:CreateGrant under %s, which lets the credential hand any principal a key it never made", tier, purpose, g.condition)
				}
			}
		}
	}
}

func TestEveryCredentialPublishesAndReclaimsOnlyTheRuntimeLayers(t *testing.T) {
	for _, tier := range bothTiers {
		r := defaultNamespace.ScopedARNs(tier)
		for purpose, document := range credentialsOfTier(t, tier) {
			testRuntimeLayerGrants(t, string(tier)+" "+purpose, document, r)
		}
	}
}

func testRuntimeLayerGrants(t *testing.T, purpose, document string, r ScopedARNs) {
	t.Helper()
	grants := grantsOf(t, document)
	for _, action := range []string{"lambda:PublishLayerVersion", "lambda:DeleteLayerVersion", "lambda:GetLayerVersion"} {
		for _, version := range r.runtimeLayerVersions {
			if !grants[grant{action: action, resource: version, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential does not grant %s on %s, so the runtime stack cannot publish or reclaim a layer version", purpose, action, version)
			}
		}
	}
	for g := range grants {
		if !strings.Contains(g.action, "LayerVersion") {
			continue
		}
		if !slices.Contains(r.runtimeLayers, g.resource) && !slices.Contains(r.runtimeLayerVersions, g.resource) {
			t.Errorf("the %s credential grants %s on %s, which reaches layers Ocel never published", purpose, g.action, g.resource)
		}
	}
}

func TestTheDeployCredentialAnswersAndShieldsThePublicFrontOnlyOnWhatItTagged(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	want := []grant{
		{action: "elasticloadbalancing:AddListenerCertificates", resource: containerListenerARN, condition: conditionJSON(t, taggedByOcel())},
		{action: "elasticloadbalancing:RemoveListenerCertificates", resource: containerListenerARN, condition: conditionJSON(t, taggedByOcel())},
		{action: "elasticloadbalancing:CreateTrustStore", resource: containerTrustStoreARN, condition: conditionJSON(t, taggedOnCreate())},
		{action: "elasticloadbalancing:ModifyTrustStore", resource: containerTrustStoreARN, condition: conditionJSON(t, taggedByOcel())},
		{action: "elasticloadbalancing:AddTags", resource: containerTrustStoreARN, condition: conditionJSON(t, taggedByOcel())},
	}
	for _, wanted := range want {
		if !grants[wanted] {
			t.Errorf("the deploy credential does not grant %s on %s under %s: a hostname forwarded to the public load balancer needs its certificate attached and the edge's client certificate trusted", wanted.action, wanted.resource, wanted.condition)
		}
	}
}

func TestTheDeployCredentialScalesOnlyTheECSServicesItTagged(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	onCreate := conditionJSON(t, scalesECSServices(taggedOnCreate()))
	tagged := conditionJSON(t, scalesECSServices(taggedByOcel()))
	want := []grant{
		{action: "application-autoscaling:RegisterScalableTarget", resource: scalableTargetARN, condition: onCreate},
		{action: "application-autoscaling:RegisterScalableTarget", resource: scalableTargetARN, condition: tagged},
		{action: "application-autoscaling:DeregisterScalableTarget", resource: scalableTargetARN, condition: tagged},
		{action: "application-autoscaling:PutScalingPolicy", resource: scalableTargetARN, condition: tagged},
		{action: "application-autoscaling:DeleteScalingPolicy", resource: scalableTargetARN, condition: tagged},
		{action: "application-autoscaling:TagResource", resource: scalableTargetARN, condition: conditionJSON(t, taggedOnCreate())},
		{action: "application-autoscaling:TagResource", resource: scalableTargetARN, condition: conditionJSON(t, taggedByOcel())},
		{action: "application-autoscaling:UntagResource", resource: scalableTargetARN, condition: conditionJSON(t, taggedByOcel())},
		{action: "cloudwatch:PutMetricAlarm", resource: scalingAlarmARN, condition: "null"},
		{action: "cloudwatch:DeleteAlarms", resource: scalingAlarmARN, condition: "null"},
		{action: "iam:CreateServiceLinkedRole", resource: ecsScalingLinkedRoleARN, condition: conditionJSON(t, linkedRoleFor("ecs.application-autoscaling.amazonaws.com"))},
	}
	for _, wanted := range want {
		if !grants[wanted] {
			t.Errorf("the deploy credential does not grant %s on %s under %s: a container app that scales between its instance counts needs its service registered and a request policy put on it", wanted.action, wanted.resource, wanted.condition)
		}
	}
}

type ecrRequest struct {
	action           string
	principalProject string
	repository       string
	requestProject   string
	resourceProject  string
}

func (r ecrRequest) context() map[string]string {
	context := map[string]string{
		"aws:PrincipalAccount": "123456789012",
		"aws:ResourceAccount":  "123456789012",
	}
	if r.principalProject != "" {
		context["aws:PrincipalTag/ocel:project"] = r.principalProject
	}
	if r.requestProject != "" {
		context["aws:RequestTag/ocel:project"] = r.requestProject
		context["aws:RequestTag/ocel:managed-by"] = "ocel"
	}
	if r.resourceProject != "" {
		context["aws:ResourceTag/ocel:project"] = r.resourceProject
		context["aws:ResourceTag/ocel:managed-by"] = "ocel"
	}
	return context
}

var policyVariable = regexp.MustCompile(`\$\{([^}]*)\}`)

func resolveVariables(t *testing.T, value string, context map[string]string) (string, bool) {
	t.Helper()
	resolved := true
	out := policyVariable.ReplaceAllStringFunc(value, func(variable string) string {
		key := policyVariable.FindStringSubmatch(variable)[1]
		if strings.Contains(key, ",") {
			t.Fatalf("%s gives a policy variable a default, whose matching IAM documents nowhere: name what each principal may reach in statements of its own", value)
		}
		got, ok := context[key]
		resolved = resolved && ok
		return got
	})
	return out, resolved
}

func allowsECR(t *testing.T, document string, r ecrRequest) bool {
	t.Helper()
	context := r.context()
	arn := "arn:aws:ecr:us-east-1:123456789012:repository/" + r.repository
	for _, statement := range parsePolicy(t, document).Statement {
		if !slices.Contains(stringsOf(t, statement.Action, "Action"), r.action) {
			continue
		}
		matches := slices.ContainsFunc(stringsOf(t, statement.Resource, "Resource"), func(resource string) bool {
			resolved, ok := resolveVariables(t, resource, context)
			return ok && conditionAdmits("StringLike", resolved, arn)
		})
		if matches && conditionHolds(t, statement.Condition, context) {
			return true
		}
	}
	return false
}

func TestEveryCredentialReachesImageRepositoriesOnlyOfTheProjectItsPrincipalIsTaggedWith(t *testing.T) {
	cases := []struct {
		name    string
		request ecrRequest
		allowed bool
	}{
		{"an untagged principal deletes in an Ocel repository tagged with a project", ecrRequest{action: "ecr:BatchDeleteImage", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"an untagged principal removes an Ocel repository tagged with a project", ecrRequest{action: "ecr:DeleteRepository", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"an untagged principal deletes in a repository no project is tagged on", ecrRequest{action: "ecr:BatchDeleteImage", repository: "ocel/shop.web"}, false},
		{"an untagged principal creates a repository tagged with its project", ecrRequest{action: "ecr:CreateRepository", repository: "ocel/shop.web", requestProject: "shop"}, true},
		{"an untagged principal creates a repository with no project tag", ecrRequest{action: "ecr:CreateRepository", repository: "ocel/shop.web"}, false},
		{"an untagged principal tags a repository made before the tag existed", ecrRequest{action: "ecr:TagResource", repository: "ocel/shop.web", requestProject: "shop"}, true},
		{"a tagged principal deletes in its project's repository", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"a tagged principal removes its project's repository", ecrRequest{action: "ecr:DeleteRepository", principalProject: "shop", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"a tagged principal deletes in another project's repository", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "ocel/blog.web", resourceProject: "blog"}, false},
		{"a tagged principal deletes in another project's repository it tagged as its own", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "ocel/blog.web", resourceProject: "shop"}, false},
		{"a tagged principal deletes in its project's repository tagged with another project", ecrRequest{action: "ecr:BatchDeleteImage", principalProject: "shop", repository: "ocel/shop.web", resourceProject: "blog"}, false},
		{"a tagged principal creates its project's repository", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "ocel/shop.web", requestProject: "shop"}, true},
		{"a tagged principal creates another project's repository tagged as its own", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "ocel/blog.web", requestProject: "shop"}, false},
		{"a tagged principal creates its project's repository tagged with another project", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "ocel/shop.web", requestProject: "blog"}, false},
		{"a tagged principal creates a repository whose project only starts with its own", ecrRequest{action: "ecr:CreateRepository", principalProject: "shop", repository: "ocel/shop-two.web", requestProject: "shop"}, false},
		{"a tagged principal tags its project's repository", ecrRequest{action: "ecr:TagResource", principalProject: "shop", repository: "ocel/shop.web", requestProject: "shop"}, true},
		{"a tagged principal tags another project's repository as its own", ecrRequest{action: "ecr:TagResource", principalProject: "shop", repository: "ocel/blog.web", requestProject: "shop", resourceProject: "blog"}, false},
		{"a tagged principal pushes to its project's repository", ecrRequest{action: "ecr:PutImage", principalProject: "shop", repository: "ocel/shop.web", resourceProject: "shop"}, true},
		{"any principal deletes outside the ocel namespace", ecrRequest{action: "ecr:BatchDeleteImage", repository: "theirs/web", resourceProject: "shop"}, false},
	}
	for purpose, document := range bothCredentials(t) {
		for _, c := range cases {
			if got := allowsECR(t, document, c.request); got != c.allowed {
				t.Errorf("the %s credential: %s is allowed = %t, want %t", purpose, c.name, got, c.allowed)
			}
		}
	}
}

func conditionAdmits(operator, pattern, value string) bool {
	switch operator {
	case "StringEquals":
		return pattern == value
	case "StringLike":
		quoted := regexp.QuoteMeta(pattern)
		quoted = strings.ReplaceAll(quoted, `\*`, ".*")
		quoted = strings.ReplaceAll(quoted, `\?`, ".")
		return regexp.MustCompile("^" + quoted + "$").MatchString(value)
	}
	return false
}

func TestEveryCredentialRunsAndReachesOnlyTheTasksOfABastionCluster(t *testing.T) {
	onBastion := conditionJSON(t, map[string]any{"ArnEquals": map[string]any{"ecs:cluster": bastionClusterARN}})
	want := []grant{
		{action: "ecs:RunTask", resource: bastionTaskDefinitionARN, condition: onBastion},
		{action: "ecs:DescribeTasks", resource: bastionTaskARN, condition: "null"},
		{action: "ecs:StopTask", resource: bastionTaskARN, condition: "null"},
		{action: "ecs:ExecuteCommand", resource: bastionClusterARN, condition: "null"},
		{action: "ecs:ExecuteCommand", resource: bastionTaskARN, condition: "null"},
		{action: "ecs:ListTasks", resource: UnscopedResource, condition: onBastion},
		{action: "ecs:DescribeClusters", resource: bastionClusterARN, condition: "null"},
		{action: "ecs:DeleteTaskDefinitions", resource: bastionTaskDefinitionARN, condition: "null"},
		{action: "ssm:StartSession", resource: bastionTaskARN, condition: "null"},
		{action: "ssm:StartSession", resource: portForwardingDocumentARN, condition: "null"},
	}
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		for _, wanted := range want {
			if !grants[wanted] {
				t.Errorf("the %s credential does not grant %s on %s under %s: the bastion a build forwards ports through cannot be run and reached without it", purpose, wanted.action, wanted.resource, wanted.condition)
			}
		}
	}
}

func TestNoCredentialRunsOrReachesAnythingBeyondTheBastionScopes(t *testing.T) {
	reachable := []string{"ecs:RunTask", "ecs:StopTask", "ecs:ExecuteCommand", "ssm:StartSession", "ssm:TerminateSession"}
	scopes := []string{bastionClusterARN, bastionTaskARN, bastionTaskDefinitionARN, portForwardingDocumentARN}
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			if slices.Contains(reachable, g.action) && !slices.Contains(scopes, g.resource) {
				t.Errorf("the %s credential grants %s on %s, which reaches past the bastion cluster's tasks and the one port forwarding document", purpose, g.action, g.resource)
			}
		}
	}
}

func TestEveryCredentialListsTasksOnlyOfABastionClusterAndAgainstTheResourceAWSEvaluates(t *testing.T) {
	onBastion := conditionJSON(t, map[string]any{"ArnEquals": map[string]any{"ecs:cluster": bastionClusterARN}})
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			if g.action != "ecs:ListTasks" {
				continue
			}
			if g.resource != UnscopedResource || g.condition != onBastion {
				t.Errorf("the %s credential grants ecs:ListTasks on %s under %s, want it on %s under ecs:cluster of a bastion cluster: AWS evaluates ListTasks against a container instance, so a cluster ARN never matches", purpose, g.resource, g.condition, UnscopedResource)
			}
		}
	}
}

func TestNoCredentialTerminatesSessionsItHasNoCallFor(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		if actionsOf(t, document)["ssm:TerminateSession"] {
			t.Errorf("the %s credential grants ssm:TerminateSession, which no port forward calls: a session ends with the TerminateSession flag on its own data channel", purpose)
		}
	}
}

func TestEveryManagedByConditionAdmitsTheTagOcelWrites(t *testing.T) {
	const writtenByOcel = "ocel"
	keys := []string{"aws:RequestTag/ocel:managed-by", "aws:ResourceTag/ocel:managed-by"}
	for purpose, document := range bothCredentials(t) {
		for _, statement := range parsePolicy(t, document).Statement {
			for operator, operands := range statement.Condition {
				keyed, ok := operands.(map[string]any)
				if !ok {
					continue
				}
				for _, key := range keys {
					pattern, named := keyed[key]
					if !named {
						continue
					}
					if !conditionAdmits(operator, fmt.Sprint(pattern), writtenByOcel) {
						t.Errorf(
							"the %s credential grants %s only where %s %s %v, which the %s=%s tag every Ocel resource carries never satisfies",
							purpose, strings.Join(stringsOf(t, statement.Action, "Action"), ", "), key, operator, pattern, managedByTagKey, writtenByOcel,
						)
					}
				}
			}
		}
	}
}

func TestEveryCredentialImportsAndReclaimsOnlyTheCertificatesItTagged(t *testing.T) {
	want := map[grant]bool{
		{action: "acm:ImportCertificate", resource: appCertificateARN, condition: conditionJSON(t, taggedOnCreate())}: true,
		{action: "acm:AddTagsToCertificate", resource: appCertificateARN, condition: conditionJSON(t, map[string]any{
			"StringEquals":         map[string]any{"aws:RequestTag/ocel:managed-by": "ocel"},
			"StringEqualsIfExists": map[string]any{"acm:CertificateKeyPairOrigin": "CUSTOMER_PROVIDED"},
		})}: true,
		{action: "acm:DescribeCertificate", resource: appCertificateARN, condition: conditionJSON(t, taggedByOcel())}:       true,
		{action: "acm:ListTagsForCertificate", resource: appCertificateARN, condition: conditionJSON(t, taggedByOcel())}:    true,
		{action: "acm:AddTagsToCertificate", resource: appCertificateARN, condition: conditionJSON(t, taggedByOcel())}:      true,
		{action: "acm:RemoveTagsFromCertificate", resource: appCertificateARN, condition: conditionJSON(t, taggedByOcel())}: true,
		{action: "acm:DeleteCertificate", resource: appCertificateARN, condition: conditionJSON(t, map[string]any{
			"StringEquals":         map[string]any{"aws:ResourceTag/ocel:managed-by": "ocel"},
			"StringEqualsIfExists": map[string]any{"acm:CertificateKeyPairOrigin": "CUSTOMER_PROVIDED"},
		})}: true,
	}
	for purpose, document := range bothCredentials(t) {
		granted := map[grant]bool{}
		for g := range grantsOf(t, document) {
			if strings.HasPrefix(g.action, "acm:") {
				granted[g] = true
			}
		}
		for wanted := range want {
			if !granted[wanted] {
				t.Errorf("the %s credential does not grant %s on %s under %s: the public load balancer's default certificate is imported into ACM", purpose, wanted.action, wanted.resource, wanted.condition)
			}
		}
		for g := range granted {
			if !want[g] {
				t.Errorf("the %s credential grants %s on %s under %s, which reaches certificates Ocel never imported", purpose, g.action, g.resource, g.condition)
			}
		}
	}
}

func TestEveryCredentialRunsEventAPIsOnlyOnWhatItTagged(t *testing.T) {
	apis, namespaces := "arn:aws:appsync:*:*:apis/*", "arn:aws:appsync:*:*:apis/*/channelNamespace/*"
	want := map[grant]bool{
		{action: "appsync:CreateApi", resource: UnscopedResource, condition: conditionJSON(t, taggedOnCreate())}:        true,
		{action: "appsync:CreateChannelNamespace", resource: namespaces, condition: conditionJSON(t, taggedOnCreate())}: true,
	}
	for _, action := range []string{
		"appsync:DeleteApi",
		"appsync:DeleteChannelNamespace",
		"appsync:GetApi",
		"appsync:GetChannelNamespace",
		"appsync:ListTagsForResource",
		"appsync:TagResource",
		"appsync:UntagResource",
		"appsync:UpdateApi",
		"appsync:UpdateChannelNamespace",
	} {
		for _, resource := range []string{apis, namespaces} {
			want[grant{action: action, resource: resource, condition: conditionJSON(t, taggedByOcel())}] = true
		}
	}
	for purpose, document := range bothCredentials(t) {
		granted := map[grant]bool{}
		for g := range grantsOf(t, document) {
			if strings.HasPrefix(g.action, "appsync:") {
				granted[g] = true
			}
		}
		if !maps.Equal(granted, want) {
			t.Errorf("the %s credential grants appsync %v, want exactly %v: an Event API and its namespaces created tagged, and touched only while tagged", purpose, granted, want)
		}
	}
}

func TestEveryCredentialKeepsRealtimeSigningKeysUnderTheirRootAlone(t *testing.T) {
	keys := "arn:aws:secretsmanager:*:*:secret:" + defaultNamespace.SigningKeyRoot() + "/*"
	want := map[grant]bool{
		{action: "secretsmanager:CreateSecret", resource: keys, condition: conditionJSON(t, taggedOnCreate())}: true,
		{action: "secretsmanager:TagResource", resource: keys, condition: conditionJSON(t, taggedOnCreate())}:  true,
		{action: "secretsmanager:ListSecrets", resource: UnscopedResource, condition: conditionJSON(t, nil)}:   true,
	}
	for _, action := range []string{"secretsmanager:DeleteSecret", "secretsmanager:GetSecretValue"} {
		want[grant{action: action, resource: keys, condition: conditionJSON(t, taggedByOcel())}] = true
	}
	for purpose, document := range bothCredentials(t) {
		granted := map[grant]bool{}
		for g := range grantsOf(t, document) {
			if strings.HasPrefix(g.action, "secretsmanager:") && g.resource != appSecretARN {
				granted[g] = true
			}
		}
		if !maps.Equal(granted, want) {
			t.Errorf("the %s credential grants secretsmanager %v beyond the RDS master secrets, want exactly %v", purpose, granted, want)
		}
	}
}

func TestBothCredentialsAdmitABroughtVariablesKeyByItsARNToWhatOcelCallsOnItAndNothingMore(t *testing.T) {
	const brought = "arn:aws:kms:us-east-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	want := map[grant]bool{
		{action: "kms:Decrypt", resource: brought, condition: conditionJSON(t, nil)}:     true,
		{action: "kms:DescribeKey", resource: brought, condition: conditionJSON(t, nil)}: true,
		{action: "kms:Encrypt", resource: brought, condition: conditionJSON(t, nil)}:     true,
		{action: "kms:CreateGrant", resource: brought, condition: conditionJSON(t, map[string]any{
			"Bool": map[string]any{"kms:GrantIsForAWSResource": "true"},
		})}: true,
	}
	for purpose, render := range map[string]func(Namespace, environment.Tier, string) (string, error){
		"bootstrap": BootstrapCredentialPermissions,
		"deploy":    DeployCredentialPermissions,
	} {
		without := grantsOf(t, mustRender(t, render))
		document, err := render(defaultNamespace, environment.TierProduction, brought)
		if err != nil {
			t.Fatalf("render the %s credential with a brought key: %v", purpose, err)
		}
		with := grantsOf(t, document)

		for g := range without {
			if !with[g] {
				t.Errorf("the %s credential drops %s on %s when a key is brought", purpose, g.action, g.resource)
			}
		}
		added := map[grant]bool{}
		for g := range with {
			if !without[g] {
				added[g] = true
			}
		}
		if !maps.Equal(added, want) {
			t.Errorf("the %s credential adds %v for a brought key, want %v", purpose, added, want)
		}
	}
}

var bothTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func credentialsOfTier(t *testing.T, tier environment.Tier) map[string]string {
	t.Helper()
	bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
	return map[string]string{"bootstrap": bootstrapDoc, "deploy": deployDoc}
}

func reaches(t *testing.T, document, action, arn string) bool {
	t.Helper()
	service, _, _ := strings.Cut(action, ":")
	for _, statement := range parsePolicy(t, document).Statement {
		actions := stringsOf(t, statement.Action, "Action")
		if !slices.Contains(actions, action) && !slices.Contains(actions, service+":*") {
			continue
		}
		matched := slices.ContainsFunc(stringsOf(t, statement.Resource, "Resource"), func(pattern string) bool {
			return iamResourceMatches(pattern, arn)
		})
		if matched {
			return true
		}
	}
	return false
}

func TestEveryCredentialOpensOnlyTheVariablesKeyOfItsOwnTier(t *testing.T) {
	opening := []string{"kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey", "kms:CreateGrant"}
	for _, tier := range bothTiers {
		want := map[string]any{"kms:ResourceAliases": defaultNamespace.variablesKeyAliasFor(tier)}
		for purpose, document := range credentialsOfTier(t, tier) {
			opened := map[string]bool{}
			for _, statement := range parsePolicy(t, document).Statement {
				for _, action := range stringsOf(t, statement.Action, "Action") {
					if !slices.Contains(opening, action) {
						continue
					}
					opened[action] = true
					if got := statement.Condition["ForAnyValue:StringEquals"]; !reflect.DeepEqual(got, want) {
						t.Errorf("the %s %s credential grants %s under ForAnyValue:StringEquals %v, want %v: any other key reaches the %s tier's variables", tier, purpose, action, got, want, tier.Sibling())
					}
				}
			}
			for _, action := range opening {
				if !opened[action] {
					t.Errorf("the %s %s credential grants no %s, and a deploy seals and opens variables with it", tier, purpose, action)
				}
			}
		}
	}
}

func TestEveryCredentialMintsAppRolesUnderOnlyTheBoundaryOfItsOwnTier(t *testing.T) {
	for _, tier := range bothTiers {
		for purpose, document := range credentialsOfTier(t, tier) {
			pinned := false
			for _, statement := range parsePolicy(t, document).Statement {
				if !slices.Contains(stringsOf(t, statement.Action, "Action"), "iam:CreateRole") || !boundaryScopes(statement.Condition) {
					continue
				}
				pinned = true
				operands, _ := statement.Condition["StringEquals"].(map[string]any)
				if got, want := operands["iam:PermissionsBoundary"], appBoundaryARNFor(defaultNamespace, tier); got != want {
					t.Errorf("the %s %s credential mints roles under iam:PermissionsBoundary %v, want exactly %s", tier, purpose, got, want)
				}
			}
			if !pinned {
				t.Errorf("the %s %s credential mints app roles without pinning a boundary", tier, purpose)
			}
		}
	}
}

func TestEveryCredentialReachesOnlyTheStateAndVariablesOfItsOwnTier(t *testing.T) {
	stack := map[environment.Tier]string{}
	for _, tier := range bothTiers {
		stack[tier], _ = defaultNamespace.StackNameFor(tier)
	}
	const tableARN = "arn:aws:dynamodb:us-east-1:111122223333:table/"
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		for purpose, document := range credentialsOfTier(t, tier) {
			for _, c := range []struct{ action, own, other string }{
				{"s3:GetObject", "arn:aws:s3:::" + stack[tier] + "-statebucket-1a2b3c4d5e6f/state", "arn:aws:s3:::" + stack[sibling] + "-statebucket-1a2b3c4d5e6f/state"},
				{"s3:ListBucket", "arn:aws:s3:::" + stack[tier] + "-statebucket-1a2b3c4d5e6f", "arn:aws:s3:::" + stack[sibling] + "-statebucket-1a2b3c4d5e6f"},
				{"s3:PutObject", "arn:aws:s3:::" + stack[tier] + "-artifactbucket-1a2b3c4d5e6f/code.zip", "arn:aws:s3:::" + stack[sibling] + "-artifactbucket-1a2b3c4d5e6f/code.zip"},
				{"s3:PutObject", "arn:aws:s3:::" + stack[tier] + "-assetbucket-1a2b3c4d5e6f/asset.js", "arn:aws:s3:::" + stack[sibling] + "-assetbucket-1a2b3c4d5e6f/asset.js"},
				{"dynamodb:GetItem", tableARN + stack[tier] + "-VariablesTable-1A2B3C4D5E6F", tableARN + stack[sibling] + "-VariablesTable-1A2B3C4D5E6F"},
				{"dynamodb:Query", tableARN + stack[tier] + "-VariablesTable-1A2B3C4D5E6F/index/gsi1", tableARN + stack[sibling] + "-VariablesTable-1A2B3C4D5E6F/index/gsi1"},
				{"dynamodb:PutItem", tableARN + stack[tier] + "-StateTable-1A2B3C4D5E6F", tableARN + stack[sibling] + "-StateTable-1A2B3C4D5E6F"},
			} {
				if !reaches(t, document, c.action, c.own) {
					t.Errorf("the %s %s credential cannot %s on %s, its own tier's store", tier, purpose, c.action, c.own)
				}
				if reaches(t, document, c.action, c.other) {
					t.Errorf("the %s %s credential can %s on %s, the %s tier's store", tier, purpose, c.action, c.other, sibling)
				}
			}
		}
	}
}

func TestEveryBucketAndTableABootstrapStackMakesIsOneTheCredentialsName(t *testing.T) {
	for _, tier := range bothTiers {
		templates := map[string]string{"core": coreStackTemplate(defaultNamespace, tier, "")}
		for _, f := range featureRegistry {
			templates[f.name] = featureTemplate(f.name, tier)
		}
		for stack, body := range templates {
			for logicalID, resource := range parseVariablesTemplate(t, body).Resources {
				switch {
				case resource.Type == "AWS::S3::Bucket" && (stack != "core" || !slices.Contains(coreStackBuckets, logicalID)),
					resource.Type == "AWS::DynamoDB::Table" && (stack != "core" || !slices.Contains(coreStackTables, logicalID)):
					t.Errorf("the %s %s stack makes %s %s, whose generated name no credential document reaches", tier, stack, resource.Type, logicalID)
				}
			}
		}
	}
}

func TestEveryRoleABootstrapStackGeneratesIsOneOnlyItsOwnTiersCredentialMayPass(t *testing.T) {
	passing := []requestContext{
		{"iam:PassedToService": LambdaServicePrincipal},
		{"iam:PassedToService": schedulerServicePrincipal},
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		ownDoc, _ := renderedCredentialsOf(t, tier)
		siblingDoc, _ := renderedCredentialsOf(t, sibling)
		templates := map[string]string{"core": coreStackTemplate(defaultNamespace, tier, "")}
		for _, f := range featureRegistry {
			templates[f.stackName(defaultNamespace, tier)] = featureTemplate(f.name, tier)
		}
		for stack, body := range templates {
			for logicalID, resource := range parseVariablesTemplate(t, body).Resources {
				if resource.Type != "AWS::IAM::Role" || resource.Properties.RoleName != "" {
					continue
				}
				path := resource.Properties.Path
				if path == "" {
					path = "/"
				}
				role := "arn:aws:iam::111122223333:role" + path + stack + "-" + logicalID + "-A1B2C3D4E5F6"
				passable := slices.ContainsFunc(passing, func(request requestContext) bool {
					return allows(t, ownDoc, "iam:PassRole", role, taggedWithTier(tier, request))
				})
				if !passable {
					t.Errorf("the %s bootstrap credential cannot pass %s, the role its %s stack generates as %s", tier, role, stack, logicalID)
				}
				for _, request := range passing {
					if allows(t, siblingDoc, "iam:PassRole", role, taggedWithTier(sibling, request)) {
						t.Errorf("the %s bootstrap credential can pass %s to %s, the role the %s tier's %s stack generates", sibling, role, request["iam:PassedToService"], tier, stack)
					}
				}
			}
		}
	}
}

func TestOnlyTheBootstrapCredentialOfATierManagesItsBoundaryPolicyAndVariablesAlias(t *testing.T) {
	const account = "arn:aws:%s::111122223333:%s"
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		for _, c := range []struct{ action, own, other string }{
			{"iam:CreatePolicyVersion", fmt.Sprintf(account, "iam", "policy/"+defaultNamespace.AppBoundaryNameFor(tier)), fmt.Sprintf(account, "iam", "policy/"+defaultNamespace.AppBoundaryNameFor(sibling))},
			{"kms:UpdateAlias", fmt.Sprintf(account, "kms", defaultNamespace.variablesKeyAliasFor(tier)), fmt.Sprintf(account, "kms", defaultNamespace.variablesKeyAliasFor(sibling))},
		} {
			if !reaches(t, bootstrapDoc, c.action, c.own) {
				t.Errorf("the %s bootstrap credential cannot %s on %s", tier, c.action, c.own)
			}
			if reaches(t, bootstrapDoc, c.action, c.other) {
				t.Errorf("the %s bootstrap credential can %s on %s, the %s tier's", tier, c.action, c.other, sibling)
			}
			if reaches(t, deployDoc, c.action, c.own) {
				t.Errorf("the %s deploy credential can %s on %s", tier, c.action, c.own)
			}
		}
	}
}

func TestACredentialIsRefusedForATierItDoesNotServe(t *testing.T) {
	for _, render := range []func(Namespace, environment.Tier, string) (string, error){DeployCredentialPermissions, BootstrapCredentialPermissions} {
		_, err := render(defaultNamespace, "staging", "")
		if err == nil || !strings.Contains(err.Error(), `"staging"`) {
			t.Errorf("render for tier staging error = %v, want it to name the tier", err)
		}
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("render for tier staging error = %#v, want a %s refusal the CLI reports as the caller's mistake", err, refusal.CodeInvalid)
		}
		if _, err := render(defaultNamespace, "", ""); err == nil {
			t.Error("render for no tier produced a document, want a refusal: it would reach both tiers")
		}
	}
}

type requestContext map[string]string

func allows(t *testing.T, document, action, arn string, request requestContext) bool {
	t.Helper()
	service, _, _ := strings.Cut(action, ":")
	for _, statement := range parsePolicy(t, document).Statement {
		actions := stringsOf(t, statement.Action, "Action")
		if !slices.Contains(actions, action) && !slices.Contains(actions, service+":*") {
			continue
		}
		if !slices.ContainsFunc(stringsOf(t, statement.Resource, "Resource"), func(pattern string) bool {
			return iamResourceMatches(pattern, arn)
		}) {
			continue
		}
		if namesAResourceTagAWSIgnoresFor(action, statement.Condition) {
			continue
		}
		if conditionHolds(t, statement.Condition, request) {
			return true
		}
	}
	return false
}

func namesAResourceTagAWSIgnoresFor(action string, condition map[string]any) bool {
	if action != "iam:PassRole" {
		return false
	}
	for _, operands := range condition {
		for key := range operands.(map[string]any) {
			if strings.HasPrefix(key, "aws:ResourceTag/") || strings.HasPrefix(key, "iam:ResourceTag/") {
				return true
			}
		}
	}
	return false
}

func conditionHolds(t *testing.T, condition map[string]any, request requestContext) bool {
	t.Helper()
	for operator, operands := range condition {
		base, ifExists := strings.CutSuffix(operator, "IfExists")
		for key, operand := range operands.(map[string]any) {
			var wanted []string
			switch v := operand.(type) {
			case string:
				wanted = []string{v}
			case []any:
				for _, each := range v {
					wanted = append(wanted, each.(string))
				}
			default:
				t.Fatalf("condition %s %s has operand %v of type %T", operator, key, operand, operand)
			}
			got, present := request[key]
			if base == "Null" {
				if len(wanted) != 1 || (wanted[0] == "true") == present {
					return false
				}
				continue
			}
			if !present {
				if ifExists {
					continue
				}
				return false
			}
			if !slices.ContainsFunc(wanted, func(pattern string) bool {
				resolved, ok := resolveVariables(t, pattern, request)
				return ok && operatorAdmits(t, base, resolved, got)
			}) {
				return false
			}
		}
	}
	return true
}

func operatorAdmits(t *testing.T, operator, pattern, value string) bool {
	t.Helper()
	switch operator {
	case "StringEquals", "ArnEquals", "Bool", "ForAnyValue:StringEquals":
		return pattern == value
	case "StringLike", "ArnLike":
		return conditionAdmits("StringLike", pattern, value)
	}
	t.Fatalf("condition operator %s has no evaluation in this test", operator)
	return false
}

const variablesKeyARN = "arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"

func variablesKeyOf(tier environment.Tier) requestContext {
	return requestContext{
		"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue,
		"aws:ResourceTag/" + naming.EnvTierTagKey:        string(tier),
		"kms:ResourceAliases":                            defaultNamespace.variablesKeyAliasFor(tier),
	}
}

func TestABootstrapCredentialRetargetsAndManagesOnlyTheVariablesKeyOfItsOwnTier(t *testing.T) {
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		for _, action := range []string{
			"kms:CreateAlias", "kms:UpdateAlias", "kms:DeleteAlias",
			"kms:PutKeyPolicy", "kms:ScheduleKeyDeletion", "kms:DisableKey", "kms:TagResource", "kms:UntagResource",
		} {
			if !allows(t, bootstrapDoc, action, variablesKeyARN, variablesKeyOf(tier)) {
				t.Errorf("the %s bootstrap credential cannot %s on its own tier's variables key", tier, action)
			}
			if allows(t, bootstrapDoc, action, variablesKeyARN, variablesKeyOf(sibling)) {
				t.Errorf("the %s bootstrap credential can %s on the %s tier's variables key, so it can point its own alias at that key or take the key over", tier, action, sibling)
			}
		}
	}
}

func taggedWithTier(tier environment.Tier, also requestContext) requestContext {
	request := requestContext{"aws:ResourceTag/" + naming.EnvTierTagKey: string(tier)}
	maps.Copy(request, also)
	return request
}

func TestABootstrapCredentialReachesTheRolesAndFunctionsOfOnlyItsOwnTier(t *testing.T) {
	const function = "arn:aws:lambda:us-east-1:111122223333:function:ocel-bootstrap-variables-k-EnvSourceSync-A1B2C3D4E5F6"
	roleOf := func(tier environment.Tier) string {
		return "arn:aws:iam::111122223333:role" + defaultNamespace.bootstrapRolePathFor(tier) + "ocel-bootstrap-variables-k-EnvSourceSyncRol-A1B2C3D4E5F6"
	}
	toLambda := requestContext{"iam:PassedToService": LambdaServicePrincipal}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		role, siblingRole := roleOf(tier), roleOf(sibling)
		for _, c := range []struct {
			action, arn, siblingARN string
			also                    requestContext
		}{
			{"iam:PutRolePolicy", role, siblingRole, nil},
			{"iam:DeleteRolePolicy", role, siblingRole, nil},
			{"iam:UpdateAssumeRolePolicy", role, siblingRole, nil},
			{"iam:UpdateRole", role, siblingRole, nil},
			{"iam:DeleteRole", role, siblingRole, nil},
			{"iam:UntagRole", role, siblingRole, nil},
			{"iam:TagRole", role, siblingRole, nil},
			{"iam:AttachRolePolicy", role, siblingRole, requestContext{"iam:PolicyARN": LambdaBasicExecutionPolicyARN}},
			{"iam:PassRole", role, siblingRole, toLambda},
			{"lambda:UpdateFunctionCode", function, function, nil},
			{"lambda:UpdateFunctionConfiguration", function, function, nil},
			{"lambda:AddPermission", function, function, nil},
			{"lambda:DeleteFunction", function, function, nil},
			{"lambda:PutFunctionEventInvokeConfig", function, function, nil},
			{"lambda:TagResource", function, function, nil},
		} {
			if !allows(t, bootstrapDoc, c.action, c.arn, taggedWithTier(tier, c.also)) {
				t.Errorf("the %s bootstrap credential cannot %s on %s, which its own stacks made", tier, c.action, c.arn)
			}
			if allows(t, bootstrapDoc, c.action, c.siblingARN, taggedWithTier(sibling, c.also)) {
				t.Errorf("the %s bootstrap credential can %s on %s, which the %s tier's stacks made", tier, c.action, c.siblingARN, sibling)
			}
		}
		for _, c := range []struct{ action, arn string }{
			{"iam:CreateRole", role},
			{"lambda:CreateFunction", function},
		} {
			creating := func(of environment.Tier) requestContext {
				return requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(of)}
			}
			if !allows(t, bootstrapDoc, c.action, c.arn, creating(tier)) {
				t.Errorf("the %s bootstrap credential cannot %s tagged for its own tier", tier, c.action)
			}
			if allows(t, bootstrapDoc, c.action, c.arn, creating(sibling)) {
				t.Errorf("the %s bootstrap credential can %s tagged for the %s tier", tier, c.action, sibling)
			}
		}
		retag := taggedWithTier(tier, requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(sibling)})
		for _, c := range []struct{ action, arn string }{{"iam:TagRole", role}, {"lambda:TagResource", function}} {
			if allows(t, bootstrapDoc, c.action, c.arn, retag) {
				t.Errorf("the %s bootstrap credential can %s on its own %s as the %s tier's", tier, c.action, c.arn, sibling)
			}
		}
	}
}

func TestABootstrapCredentialReachesTheNamedBootstrapResourcesOfOnlyItsOwnTier(t *testing.T) {
	const account = "us-east-1:111122223333"
	named := func(tier environment.Tier) map[string]string {
		feature := defaultNamespace.featureStackName(provider.FeatureVariablesKey, tier)
		core, _ := defaultNamespace.StackNameFor(tier)
		edgeUser, _ := defaultNamespace.EdgeUserNameFor(tier)
		group := defaultNamespace.envSourceSyncScheduleGroupName(tier)
		return map[string]string{
			"cloudformation:DeleteStack":     "arn:aws:cloudformation:" + account + ":stack/" + core + "/0a1b",
			"cloudformation:CreateChangeSet": "arn:aws:cloudformation:" + account + ":stack/" + feature + "/0a1b",
			"logs:DeleteLogGroup":            "arn:aws:logs:" + account + ":log-group:/aws/lambda/" + feature + "-EnvSourceSync",
			"scheduler:UpdateSchedule":       "arn:aws:scheduler:" + account + ":schedule/" + group + "/" + defaultNamespace.envSourceSyncScheduleName(tier),
			"scheduler:DeleteScheduleGroup":  "arn:aws:scheduler:" + account + ":schedule-group/" + group,
			"iam:PutUserPolicy":              "arn:aws:iam::111122223333:user/" + edgeUser,
			"lambda:PublishLayerVersion":     "arn:aws:lambda:" + account + ":layer:" + runtimeLayerName(defaultNamespace, tier, arch.ARM64, "0123456789abcdef"),
		}
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		own, other := named(tier), named(sibling)
		for action, arn := range own {
			if !allows(t, bootstrapDoc, action, arn, nil) {
				t.Errorf("the %s bootstrap credential cannot %s on %s, its own", tier, action, arn)
			}
			if tier == environment.TierPreview && allows(t, bootstrapDoc, action, other[action], nil) {
				t.Errorf("the preview bootstrap credential can %s on %s, the production tier's", action, other[action])
			}
		}
	}
}

func appResourceOf(tier environment.Tier, also requestContext) requestContext {
	request := taggedWithTier(tier, requestContext{"aws:ResourceTag/" + managedByTagKey: managedByTagValue})
	maps.Copy(request, also)
	return request
}

func TestACredentialReachesTheAppFunctionsAndRolesOfOnlyItsOwnTier(t *testing.T) {
	const (
		function = "arn:aws:lambda:us-east-1:111122223333:function:ocel-app-shop-pr-7-web-a1b2c3"
		mapping  = "arn:aws:lambda:us-east-1:111122223333:event-source-mapping:0a1b2c3d-4e5f-6789-abcd-ef0123456789"
	)
	roleOf := func(tier environment.Tier) string {
		return "arn:aws:iam::111122223333:role" + defaultNamespace.AppRolePathFor(tier) + "ocel-app-shop-pr-7-web-role-a1b2c3"
	}
	worker := requestContext{"lambda:FunctionArn": "arn:aws:lambda:us-east-1:111122223333:function:ocel-app-shop-pr-7-worker-a1b2c3"}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		role, siblingRole := roleOf(tier), roleOf(sibling)
		for purpose, document := range credentialsOfTier(t, tier) {
			for _, c := range []struct {
				action, arn, siblingARN string
				also                    requestContext
			}{
				{"lambda:UpdateFunctionCode", function, function, nil},
				{"lambda:UpdateFunctionConfiguration", function, function, nil},
				{"lambda:InvokeFunction", function, function, nil},
				{"lambda:AddPermission", function, function, nil},
				{"lambda:DeleteFunction", function, function, nil},
				{"lambda:TagResource", function, function, nil},
				{"iam:PassRole", role, siblingRole, requestContext{"iam:PassedToService": LambdaServicePrincipal}},
				{"iam:PassRole", role, siblingRole, requestContext{"iam:PassedToService": ecsTasksPrincipal}},
				{"iam:PassRole", role, siblingRole, requestContext{"iam:PassedToService": schedulerServicePrincipal}},
				{"iam:DeleteRole", role, siblingRole, nil},
				{"iam:UpdateRole", role, siblingRole, nil},
				{"iam:TagRole", role, siblingRole, nil},
				{"lambda:UpdateEventSourceMapping", mapping, mapping, worker},
				{"lambda:DeleteEventSourceMapping", mapping, mapping, worker},
				{"lambda:TagResource", mapping, mapping, nil},
			} {
				if !allows(t, document, c.action, c.arn, appResourceOf(tier, c.also)) {
					t.Errorf("the %s %s credential cannot %s on %s, which its own tier's deploys made", tier, purpose, c.action, c.arn)
				}
				if allows(t, document, c.action, c.siblingARN, appResourceOf(sibling, c.also)) {
					t.Errorf("the %s %s credential can %s on %s, which the %s tier's deploys made", tier, purpose, c.action, c.siblingARN, sibling)
				}
			}
			creating := func(of environment.Tier, also requestContext) requestContext {
				request := requestContext{
					"aws:RequestTag/" + managedByTagKey:      managedByTagValue,
					"aws:RequestTag/" + naming.EnvTierTagKey: string(of),
				}
				maps.Copy(request, also)
				return request
			}
			for _, c := range []struct {
				action, arn string
				also        requestContext
			}{
				{"lambda:CreateFunction", function, nil},
				{"iam:CreateRole", role, requestContext{
					"aws:PrincipalAccount":    "111122223333",
					"iam:PermissionsBoundary": "arn:aws:iam::111122223333:policy/" + defaultNamespace.AppBoundaryNameFor(tier),
				}},
				{"lambda:CreateEventSourceMapping", UnscopedResource, worker},
			} {
				if !allows(t, document, c.action, c.arn, creating(tier, c.also)) {
					t.Errorf("the %s %s credential cannot %s tagged for its own tier", tier, purpose, c.action)
				}
				if allows(t, document, c.action, c.arn, creating(sibling, c.also)) {
					t.Errorf("the %s %s credential can %s tagged for the %s tier", tier, purpose, c.action, sibling)
				}
			}
			retag := appResourceOf(tier, requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(sibling)})
			for _, c := range []struct{ action, arn string }{{"lambda:TagResource", function}, {"iam:TagRole", role}, {"lambda:TagResource", mapping}} {
				if allows(t, document, c.action, c.arn, retag) {
					t.Errorf("the %s %s credential can %s on %s as the %s tier's", tier, purpose, c.action, c.arn, sibling)
				}
			}
		}
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		if !allows(t, bootstrapDoc, "iam:DeleteRolePermissionsBoundary", role, appResourceOf(tier, nil)) {
			t.Errorf("the %s bootstrap credential cannot lift the boundary of its own tier's app role", tier)
		}
		if allows(t, bootstrapDoc, "iam:DeleteRolePermissionsBoundary", siblingRole, appResourceOf(sibling, nil)) {
			t.Errorf("the %s bootstrap credential can lift the boundary of the %s tier's app role", tier, sibling)
		}
	}
}

func TestACredentialPassesTheBastionTaskRoleOfOnlyItsOwnTierToECS(t *testing.T) {
	toTasks := requestContext{"iam:PassedToService": ecsTasksPrincipal}
	roleOf := func(tier environment.Tier) string {
		return "arn:aws:iam::111122223333:role/" + bastion.NameFor(tier)
	}
	for _, tier := range bothTiers {
		for purpose, document := range credentialsOfTier(t, tier) {
			if !allows(t, document, "iam:PassRole", roleOf(tier), toTasks) {
				t.Errorf("the %s %s credential cannot pass %s to ECS, so its bastion task cannot be registered", tier, purpose, roleOf(tier))
			}
			if allows(t, document, "iam:PassRole", roleOf(tier.Sibling()), toTasks) {
				t.Errorf("the %s %s credential can pass %s, the %s tier's bastion role, to ECS", tier, purpose, roleOf(tier.Sibling()), tier.Sibling())
			}
		}
	}
}

func TestACredentialReachesTheParametersOfOnlyItsOwnTier(t *testing.T) {
	const account = "arn:aws:ssm:us-east-1:111122223333:parameter"
	named := func(tier environment.Tier) []string {
		origin, _ := defaultNamespace.OriginSecretParamFor(tier)
		names := []string{origin}
		for _, kind := range edgeKinds() {
			prefix, _ := defaultNamespace.EdgeParamPrefix(tier, kind)
			names = append(names, prefix+"/credentials")
		}
		return names
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
		for _, c := range []struct{ purpose, document, action string }{
			{"bootstrap", bootstrapDoc, "ssm:PutParameter"},
			{"bootstrap", bootstrapDoc, "ssm:DeleteParameter"},
			{"bootstrap", bootstrapDoc, "ssm:GetParameter"},
			{"deploy", deployDoc, "ssm:GetParameter"},
		} {
			for _, name := range named(tier) {
				if !allows(t, c.document, c.action, account+name, nil) {
					t.Errorf("the %s %s credential cannot %s on %s, its own", tier, c.purpose, c.action, name)
				}
			}
			for _, name := range named(sibling) {
				if allows(t, c.document, c.action, account+name, nil) {
					t.Errorf("the %s %s credential can %s on %s, the %s tier's", tier, c.purpose, c.action, name, sibling)
				}
			}
		}
	}
}

func TestABootstrapCredentialReachesTheQueuesOfOnlyItsOwnTier(t *testing.T) {
	const account = "arn:aws:sqs:us-east-1:111122223333:"
	named := func(tier environment.Tier) []string {
		queue, deadLetters := defaultNamespace.revalidateQueueNames(tier)
		return []string{queue, deadLetters, defaultNamespace.featureStackName(FeatureISR, tier) + "-TagInvalidatorDeadLetterQueue-A1B2C3D4E5F6"}
	}
	for _, tier := range bothTiers {
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		for _, name := range named(tier) {
			if !allows(t, bootstrapDoc, "sqs:SetQueueAttributes", account+name, nil) {
				t.Errorf("the %s bootstrap credential cannot sqs:SetQueueAttributes on %s, its own", tier, name)
			}
		}
		if tier != environment.TierPreview {
			continue
		}
		for _, name := range named(tier.Sibling()) {
			if allows(t, bootstrapDoc, "sqs:SetQueueAttributes", account+name, nil) {
				t.Errorf("the preview bootstrap credential can sqs:SetQueueAttributes on %s, the production tier's", name)
			}
		}
	}
}

func TestABootstrapCredentialMakesTheEdgeInvokeRoleOfOnlyItsOwnTier(t *testing.T) {
	role := func(tier environment.Tier) string {
		return "arn:aws:iam::111122223333:role/" + defaultNamespace.EdgeInvokeRoleName(tier)
	}
	creating := func(tier environment.Tier) requestContext {
		return requestContext{"aws:RequestTag/" + naming.EnvTierTagKey: string(tier)}
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		if !allows(t, bootstrapDoc, "iam:CreateRole", role(tier), creating(tier)) {
			t.Errorf("the %s bootstrap credential cannot create %s, which the %s stack declares", tier, role(tier), FeatureAPIGatewayEdge)
		}
		if !allows(t, bootstrapDoc, "iam:PutRolePolicy", role(tier), taggedWithTier(tier, nil)) {
			t.Errorf("the %s bootstrap credential cannot write the policy of %s", tier, role(tier))
		}
		if allows(t, bootstrapDoc, "iam:PutRolePolicy", role(sibling), taggedWithTier(sibling, nil)) {
			t.Errorf("the %s bootstrap credential can write the policy of %s, the %s tier's", tier, role(sibling), sibling)
		}
	}
}

func TestNoCredentialDeniesWhatTheOtherTiersCredentialAllows(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		for _, statement := range parsePolicy(t, document).Statement {
			if statement.Effect != "Allow" {
				t.Errorf("the %s credential has a %s statement on %s, which overrides the other tier's document on a principal that holds both", purpose, statement.Effect, statement.Resource)
			}
		}
	}
}

func TestABootstrapCredentialMakesAndTagsVariablesKeysOnlyAsItsOwnTiers(t *testing.T) {
	creating := func(tier environment.Tier) requestContext {
		return requestContext{
			"aws:RequestTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue,
			"aws:RequestTag/" + naming.EnvTierTagKey:        string(tier),
		}
	}
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		bootstrapDoc, _ := renderedCredentialsOf(t, tier)
		if !allows(t, bootstrapDoc, "kms:CreateKey", UnscopedResource, creating(tier)) {
			t.Errorf("the %s bootstrap credential cannot create its own tier's variables key", tier)
		}
		if allows(t, bootstrapDoc, "kms:CreateKey", UnscopedResource, creating(sibling)) {
			t.Errorf("the %s bootstrap credential can create a variables key tagged for the %s tier", tier, sibling)
		}
		retag := variablesKeyOf(tier)
		retag["aws:RequestTag/"+naming.EnvTierTagKey] = string(sibling)
		if allows(t, bootstrapDoc, "kms:TagResource", variablesKeyARN, retag) {
			t.Errorf("the %s bootstrap credential can retag its own variables key as the %s tier's", tier, sibling)
		}
	}
}

func TestEveryCredentialTagsTheObjectsItPutsInTheAssetBucket(t *testing.T) {
	core, err := defaultNamespace.StackNameFor(environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	object := "arn:aws:s3:::" + strings.ToLower(core) + "-assetbucket-abc123/prod/shop/web/r1a2b3c4d/assets/app.js"
	for purpose, document := range credentialsOfTier(t, environment.TierProduction) {
		if !reaches(t, document, "s3:PutObjectTagging", object) {
			t.Errorf("the %s credential cannot tag %s, so a put that tags it edge-readable is refused", purpose, object)
		}
	}
}
