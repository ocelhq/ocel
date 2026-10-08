package bootstrap

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
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
	bootstrapDoc, err := BootstrapCredentialPermissions(defaultNamespace)
	if err != nil {
		t.Fatalf("BootstrapCredentialPermissions(defaultNamespace) error = %v", err)
	}
	deployDoc, err := DeployCredentialPermissions(defaultNamespace)
	if err != nil {
		t.Fatalf("DeployCredentialPermissions(defaultNamespace) error = %v", err)
	}
	return bootstrapDoc, deployDoc
}

func bothCredentials(t *testing.T) map[string]string {
	t.Helper()
	bootstrapDoc, deployDoc := renderedCredentials(t)
	return map[string]string{"bootstrap": bootstrapDoc, "deploy": deployDoc}
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
	named, ok := operands["iam:PermissionsBoundary"].([]any)
	if !ok {
		return false
	}
	var arns []string
	for _, one := range named {
		arn, ok := one.(string)
		if !ok {
			return false
		}
		arns = append(arns, arn)
	}
	return slices.Equal(arns, []string{appBoundaryARNFor(defaultNamespace, environment.TierProduction), appBoundaryARNFor(defaultNamespace, environment.TierPreview)})
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
					if resource == defaultNamespace.ScopedARNs().bootstrapRole {
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
	r := defaultNamespace.ScopedARNs()
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
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			if g.action == "iam:AttachUserPolicy" {
				t.Errorf("the %s credential grants iam:AttachUserPolicy, which turns a minted user into whatever policy it names", purpose)
			}
			if !strings.HasPrefix(g.action, "iam:") || !strings.Contains(g.action, "User") && !strings.Contains(g.action, "AccessKey") {
				continue
			}
			if g.resource != defaultNamespace.ScopedARNs().edgeUser {
				t.Errorf("the %s credential grants %s on %q, which is not the edge user", purpose, g.action, g.resource)
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

func mustRender(t *testing.T, render func(Namespace) (string, error)) string {
	t.Helper()
	document, err := render(defaultNamespace)
	if err != nil {
		t.Fatalf("render policy: %v", err)
	}
	return document
}

func TestTheBootstrapCredentialOwnsOnlyTheLogGroupsItsStacksDeclare(t *testing.T) {
	bootstrapDoc, deployDoc := renderedCredentials(t)
	bootstrapGrants, deployGrants := grantsOf(t, bootstrapDoc), grantsOf(t, deployDoc)
	scope := "arn:aws:logs:*:*:log-group:/aws/lambda/" + defaultNamespace.CoreStackName() + "*"
	unconditional := conditionJSON(t, nil)
	want := map[string]string{
		"logs:CreateLogGroup":      unconditional,
		"logs:DeleteLogGroup":      unconditional,
		"logs:ListTagsForResource": unconditional,
		"logs:PutRetentionPolicy":  unconditional,
		"logs:TagResource":         unconditional,
		"logs:UntagResource":       unconditional,
	}
	if got := logsGrantsOn(bootstrapGrants, scope); !maps.Equal(got, want) {
		t.Errorf("the bootstrap credential grants %v on %s, want exactly %v, so a bootstrap stack either cannot create, bound and reclaim its functions' log groups or reaches beyond them", got, scope, want)
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
		switch g.resource {
		case scope, appLogGroupARN, functionLogGroupARN:
		case UnscopedResource:
			if g.action != "logs:DescribeLogGroups" {
				t.Errorf("the bootstrap credential grants %s on %q, which reaches every log group in the account", g.action, g.resource)
			}
		default:
			t.Errorf("the bootstrap credential grants %s on %s, beyond the log groups a bootstrap or a deploy owns", g.action, g.resource)
		}
	}
}

func TestEveryCredentialReachesOnlyTheBucketsAndClustersDeploysNameUnderTheAppScope(t *testing.T) {
	bootstrapARNs := defaultNamespace.ScopedARNs()
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			switch {
			case strings.HasPrefix(g.action, "s3:"):
				if g.resource == bootstrapARNs.bootstrapBucket || g.resource == bootstrapARNs.bootstrapObject {
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
	r := defaultNamespace.ScopedARNs()
	ofAppWorkers := map[string]any{"lambda:FunctionArn": "arn:aws:lambda:*:*:function:ocel-app-*"}
	allowed := map[string]bool{
		conditionJSON(t, map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": r.bootstrapFunction}}): true,
		conditionJSON(t, map[string]any{
			"StringEquals": map[string]any{"aws:RequestTag/ocel:managed-by": "ocel"},
			"ArnLike":      ofAppWorkers,
		}): true,
		conditionJSON(t, map[string]any{
			"StringEquals": map[string]any{"aws:ResourceTag/ocel:managed-by": "ocel"},
			"ArnLike":      ofAppWorkers,
		}): true,
	}
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			if !strings.HasSuffix(g.action, "EventSourceMapping") {
				continue
			}
			if !allowed[g.condition] {
				t.Errorf("the %s credential grants %s on %s under %s, want it pinned to the bootstrap's own functions or to Ocel-tagged mappings of app worker functions", purpose, g.action, g.resource, g.condition)
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
	r := defaultNamespace.ScopedARNs()
	if _, path, _ := strings.Cut(r.passphraseParam, ":parameter"); strings.ContainsAny(path, "*?") {
		t.Fatalf("the passphrase ARN %q names a parameter pattern, and the one delete grant on it must name the path exactly", r.passphraseParam)
	}
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			if !strings.HasPrefix(g.action, "ssm:Delete") || !iamResourceMatches(g.resource, r.passphraseParam) {
				continue
			}
			if purpose != "bootstrap" {
				t.Errorf("the %s credential grants %s on %s, which reaches %s: only the credential that can already destroy every Pulumi state in the account may take what encrypts it", purpose, g.action, g.resource, r.passphraseParam)
				continue
			}
			if g.action != "ssm:DeleteParameter" || g.resource != r.passphraseParam || g.condition != conditionJSON(t, nil) {
				t.Errorf("the bootstrap credential grants %s on %s under %s, want ssm:DeleteParameter on exactly %s: a wider grant reaches the passphrase by accident from a tree it was meant to prune", g.action, g.resource, g.condition, r.passphraseParam)
			}
		}
	}
	bootstrapGrants := grantsOf(t, mustRender(t, BootstrapCredentialPermissions))
	for _, resource := range []string{r.edgeParam, r.originParam, r.stackRecord, r.passphraseParam} {
		if !bootstrapGrants[grant{action: "ssm:DeleteParameter", resource: resource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the bootstrap credential cannot delete %s, which a teardown reclaims", resource)
		}
	}
}

func TestEveryCredentialMayGrantLambdaTheVariablesKeyAndNoOther(t *testing.T) {
	want := conditionJSON(t, map[string]any{
		"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
		"Bool":         map[string]any{"kms:GrantIsForAWSResource": "true"},
	})
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		if !grants[grant{action: "kms:CreateGrant", resource: AnyKeyARN, condition: want}] {
			t.Errorf("the %s credential does not grant kms:CreateGrant on a variables key for an AWS service, so Lambda cannot seal a function's environment under it", purpose)
		}
		for g := range grants {
			if g.action == "kms:CreateGrant" && g.condition != want {
				t.Errorf("the %s credential grants kms:CreateGrant under %s, which lets the credential hand any principal a key it never made", purpose, g.condition)
			}
		}
	}
}

func TestEveryCredentialPublishesAndReclaimsOnlyTheRuntimeLayers(t *testing.T) {
	r := defaultNamespace.ScopedARNs()
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		for _, action := range []string{"lambda:PublishLayerVersion", "lambda:DeleteLayerVersion", "lambda:GetLayerVersion"} {
			if !grants[grant{action: action, resource: r.runtimeLayerVersion, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential does not grant %s on %s, so the runtime stack cannot publish or reclaim a layer version", purpose, action, r.runtimeLayerVersion)
			}
		}
		for g := range grants {
			if !strings.Contains(g.action, "LayerVersion") {
				continue
			}
			if g.resource != r.runtimeLayer && g.resource != r.runtimeLayerVersion {
				t.Errorf("the %s credential grants %s on %s, which reaches layers Ocel never published", purpose, g.action, g.resource)
			}
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

func conditionHolds(t *testing.T, condition map[string]any, context map[string]string) bool {
	t.Helper()
	for operator, operands := range condition {
		base, ifExists := strings.CutSuffix(operator, "IfExists")
		for key, raw := range operands.(map[string]any) {
			values := []string{}
			switch v := raw.(type) {
			case string:
				values = append(values, v)
			case []any:
				for _, one := range v {
					values = append(values, one.(string))
				}
			}
			got, present := context[key]
			if base == "Null" {
				if len(values) != 1 || (values[0] == "true") == present {
					return false
				}
				continue
			}
			if base != "StringEquals" && base != "StringLike" {
				t.Fatalf("the ECR grants test evaluates no %s condition", operator)
			}
			if !present {
				if ifExists {
					continue
				}
				return false
			}
			if !slices.ContainsFunc(values, func(pattern string) bool {
				resolved, ok := resolveVariables(t, pattern, context)
				return ok && conditionAdmits(base, resolved, got)
			}) {
				return false
			}
		}
	}
	return true
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
