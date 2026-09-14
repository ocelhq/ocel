package bootstrap

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
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
	"lambda:DeleteLayerVersion",
	"lambda:PublishLayerVersion",
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
	"sqs:CreateQueue",
	"sqs:DeleteQueue",
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
		t.Fatal("policy carries no statement")
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

func renderedTiers(t *testing.T) (string, string) {
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

func bothTiers(t *testing.T) map[string]string {
	t.Helper()
	bootstrapDoc, deployDoc := renderedTiers(t)
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
	return slices.Equal(arns, []string{appBoundaryARNFor(defaultNamespace, ClassProduction), appBoundaryARNFor(defaultNamespace, ClassPreview)})
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

func TestNoTierMintsARoleThatCanOutgrowItsBoundary(t *testing.T) {
	for tier, document := range bothTiers(t) {
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
							"the %s tier grants %s on %q without pinning iam:PermissionsBoundary, so it can mint a role that reaches further than the credential itself",
							tier, action, resource,
						)
					}
				}
			}
		}
	}
}

func TestNoTierRepointsTheTrustPolicyOfAnAppRole(t *testing.T) {
	deployActions := actionsOf(t, mustRender(t, DeployCredentialPermissions))
	if deployActions["iam:UpdateAssumeRolePolicy"] {
		t.Error("the deploy tier grants iam:UpdateAssumeRolePolicy, which hands an app role's trust policy to whoever holds the credential")
	}
}

func TestBootstrapTierIsAStrictSupersetOfDeployTier(t *testing.T) {
	bootstrapDoc, deployDoc := renderedTiers(t)
	bootstrapGrants, deployGrants := grantsOf(t, bootstrapDoc), grantsOf(t, deployDoc)

	var missing []string
	for g := range deployGrants {
		if !bootstrapGrants[g] {
			missing = append(missing, g.action+" on "+g.resource)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("the deploy tier grants what the bootstrap tier does not: %s", strings.Join(missing, ", "))
	}

	extra := 0
	for g := range bootstrapGrants {
		if !deployGrants[g] {
			extra++
		}
	}
	if extra == 0 {
		t.Error("the bootstrap tier grants nothing the deploy tier lacks, so the two tiers are the same credential")
	}
}

func TestDeployTierWithholdsWhatDefinesTheBootstrapTier(t *testing.T) {
	bootstrapDoc, deployDoc := renderedTiers(t)
	bootstrapActions, deployActions := actionsOf(t, bootstrapDoc), actionsOf(t, deployDoc)

	for _, action := range bootstrapOnlyActions {
		if deployActions[action] {
			t.Errorf("the deploy tier grants %s, which is what a bootstrap credential is for", action)
		}
		if !bootstrapActions[action] {
			t.Errorf("the bootstrap tier no longer grants %s, so nothing holds the line at %s", action, action)
		}
	}
}

func TestDeployTierPublishesTheRuntimeStackAndNoOtherStack(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	r := defaultNamespace.ScopedARNs()
	unconditional := conditionJSON(t, nil)

	for _, action := range []string{
		"cloudformation:CreateChangeSet",
		"cloudformation:CreateStack",
		"cloudformation:DescribeStackEvents",
	} {
		if !grants[grant{action: action, resource: r.runtimeStack, condition: unconditional}] {
			t.Errorf("the deploy tier does not grant %s on %s, so a deploy onto an account an older build bootstrapped cannot publish the runtime its functions boot through", action, r.runtimeStack)
		}
	}
	for _, action := range []string{
		"cloudformation:DeleteChangeSet",
		"cloudformation:DescribeChangeSet",
		"cloudformation:ExecuteChangeSet",
	} {
		for _, resource := range []string{r.runtimeStack, r.runtimeChangeSet} {
			if !grants[grant{action: action, resource: resource, condition: unconditional}] {
				t.Errorf("the deploy tier does not grant %s on %s, so the runtime it publishes is planned and never executed", action, resource)
			}
		}
	}

	for g := range grants {
		if !strings.HasPrefix(g.action, "cloudformation:") || readOnly(g.action) {
			continue
		}
		if g.resource != r.runtimeStack && g.resource != r.runtimeChangeSet {
			t.Errorf("the deploy tier grants %s on %s, which is a bootstrap stack a deploy never writes", g.action, g.resource)
		}
	}
}

func TestDeployTierOwnsTheLogGroupsItCreates(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	want := map[string]string{
		"logs:CreateLogGroup":      conditionJSON(t, taggedOnCreate()),
		"logs:DeleteLogGroup":      conditionJSON(t, taggedByOcel()),
		"logs:ListTagsForResource": conditionJSON(t, taggedByOcel()),
		"logs:PutRetentionPolicy":  conditionJSON(t, taggedByOcel()),
		"logs:TagResource":         conditionJSON(t, taggedByOcel()),
		"logs:UntagResource":       conditionJSON(t, taggedByOcel()),
	}
	for _, resource := range []string{appLogGroupARN, functionLogGroupARN} {
		got := logsGrantsOn(grants, resource)
		if !maps.Equal(got, want) {
			t.Errorf("the deploy tier grants %v on %s, want exactly %v, so a log group is either never made with a retention, never reclaimed by the teardown, or reachable beyond what ocel tagged", got, resource, want)
		}
	}
}

func TestEveryTierListsLogGroupsOnTheOnlyResourceAWSAccepts(t *testing.T) {
	for tier, document := range bothTiers(t) {
		grants := grantsOf(t, document)
		if !grants[grant{action: "logs:DescribeLogGroups", resource: UnscopedResource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the %s tier does not grant logs:DescribeLogGroups on %q, the only resource IAM evaluates it against, so CloudFormation cannot read back a log group it manages", tier, UnscopedResource)
		}
		for g := range grants {
			if g.action == "logs:DescribeLogGroups" && g.resource != UnscopedResource {
				t.Errorf("the %s tier grants logs:DescribeLogGroups on %s, an ARN IAM never matches for an action with no resource type", tier, g.resource)
			}
		}
	}
}

func TestEveryTierScopesTaskDefinitionsToWhatAWSEvaluates(t *testing.T) {
	unscopable := []string{"ecs:DeregisterTaskDefinition", "ecs:DescribeTaskDefinition"}
	for tier, document := range bothTiers(t) {
		grants := grantsOf(t, document)
		if !grants[grant{action: "ecs:RegisterTaskDefinition", resource: appTaskDefinitionARN, condition: conditionJSON(t, taggedOnCreate())}] {
			t.Errorf("the %s tier does not grant ecs:RegisterTaskDefinition on %s under a create tag, so a container release either cannot register its family or registers one nothing marks as Ocel's", tier, appTaskDefinitionARN)
		}
		for _, action := range unscopable {
			if !grants[grant{action: action, resource: UnscopedResource, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s tier does not grant %s on %q, the only resource IAM evaluates it against, so a container release cannot read back or reclaim its task definition", tier, action, UnscopedResource)
			}
		}
		for g := range grants {
			if g.action == "ecs:RegisterTaskDefinition" && g.resource != appTaskDefinitionARN {
				t.Errorf("the %s tier grants ecs:RegisterTaskDefinition on %s, which reaches past the task definitions a deploy registers", tier, g.resource)
			}
			if slices.Contains(unscopable, g.action) && g.resource != UnscopedResource {
				t.Errorf("the %s tier grants %s on %s, an ARN IAM never matches for an action with no resource type", tier, g.action, g.resource)
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

func TestOnlyTheEdgeUserIsMintedAndItCarriesNoManagedPolicy(t *testing.T) {
	for tier, document := range bothTiers(t) {
		for g := range grantsOf(t, document) {
			if g.action == "iam:AttachUserPolicy" {
				t.Errorf("the %s tier grants iam:AttachUserPolicy, which turns a minted user into whatever policy it names", tier)
			}
			if !strings.HasPrefix(g.action, "iam:") || !strings.Contains(g.action, "User") && !strings.Contains(g.action, "AccessKey") {
				continue
			}
			if g.resource != defaultNamespace.ScopedARNs().edgeUser {
				t.Errorf("the %s tier grants %s on %q, which is not the edge user", tier, g.action, g.resource)
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
			t.Errorf("the bootstrap tier does not grant %s, which minting the edge user needs", action)
		}
	}
}

func TestCredentialTiersNameActionsRatherThanGlobbingThem(t *testing.T) {
	for tier, document := range bothTiers(t) {
		for g := range grantsOf(t, document) {
			service, verb, ok := strings.Cut(g.action, ":")
			if !ok || service == "" || strings.Contains(service, "*") {
				t.Errorf("the %s tier grants %q, which names no service", tier, g.action)
				continue
			}
			if verb == "" || strings.HasPrefix(verb, "*") {
				t.Errorf("the %s tier grants %q, whose leading wildcard stands for verbs nobody enumerated", tier, g.action)
			}
		}
	}
}

func TestEveryMutatingGrantCarriesAnOcelScope(t *testing.T) {
	for tier, document := range bothTiers(t) {
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
						"the %s tier grants %s on %q, which names nothing Ocel owns and carries no scoping condition",
						tier, strings.Join(mutating, ", "), resource,
					)
				}
			}
		}
	}
}

func TestNoTierTagsAKeyItDoesNotAlreadyOwn(t *testing.T) {
	for tier, document := range bothTiers(t) {
		for _, statement := range parsePolicy(t, document).Statement {
			actions := stringsOf(t, statement.Action, "Action")
			tagging := slices.DeleteFunc(slices.Clone(actions), func(action string) bool {
				return action != "kms:TagResource" && action != "kms:UntagResource"
			})
			if len(tagging) == 0 {
				continue
			}
			if !conditionNames(statement.Condition, "aws:ResourceTag/"+VarsKeyComponentTagKey) {
				t.Errorf(
					"the %s tier grants %s on %s with no aws:ResourceTag condition, so it may tag a key ocel never made and then open what that key seals",
					tier, strings.Join(tagging, ", "), strings.Join(stringsOf(t, statement.Resource, "Resource"), ", "),
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

func TestBootstrapTierOwnsOnlyTheLogGroupsItsStacksDeclare(t *testing.T) {
	bootstrapDoc, deployDoc := renderedTiers(t)
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
		t.Errorf("the bootstrap tier grants %v on %s, want exactly %v, so a bootstrap stack either cannot create, bound and reclaim its functions' log groups or reaches beyond them", got, scope, want)
	}
	for _, resource := range []string{appLogGroupARN, functionLogGroupARN} {
		if got, deploy := logsGrantsOn(bootstrapGrants, resource), logsGrantsOn(deployGrants, resource); !maps.Equal(got, deploy) {
			t.Errorf("the bootstrap tier grants %v on %s, want the deploy tier's %v, so bootstrapping widens what a deploy may do to a log group", got, resource, deploy)
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
				t.Errorf("the bootstrap tier grants %s on %q, which reaches every log group in the account", g.action, g.resource)
			}
		default:
			t.Errorf("the bootstrap tier grants %s on %s, beyond the log groups a bootstrap or a deploy owns", g.action, g.resource)
		}
	}
}

func TestEveryTierReachesOnlyTheBucketsAndClustersDeploysNameUnderTheAppScope(t *testing.T) {
	bootstrapARNs := defaultNamespace.ScopedARNs()
	for tier, document := range bothTiers(t) {
		for g := range grantsOf(t, document) {
			switch {
			case strings.HasPrefix(g.action, "s3:"):
				if g.resource == bootstrapARNs.bootstrapBucket || g.resource == bootstrapARNs.bootstrapObject {
					continue
				}
				if !strings.HasPrefix(g.resource, "arn:aws:s3:::"+appScopePrefix) {
					t.Errorf("the %s tier grants %s on %s, a bucket name a deploy never creates: S3 evaluates no Ocel tag on a bucket, so the name prefix is the only scope", tier, g.action, g.resource)
				}
			case strings.HasPrefix(g.action, "rds:") && !readOnly(g.action):
				for _, kind := range []string{"cluster:", "db:", "subgrp:"} {
					if strings.Contains(g.resource, ":"+kind) && !strings.Contains(g.resource, ":"+kind+appScopePrefix) {
						t.Errorf("the %s tier grants %s on %s, an identifier a deploy never mints", tier, g.action, g.resource)
					}
				}
			case strings.HasPrefix(g.action, "secretsmanager:"):
				if g.condition != conditionJSON(t, managedByAnAppCluster()) {
					t.Errorf("the %s tier grants %s on %s under %s, want the secret pinned to a cluster in the app scope through the tag RDS stamps on it, or the credential reads every Aurora master password in the account", tier, g.action, g.resource, g.condition)
				}
			}
		}
	}
}

func TestTheBootstrapTierTouchesOnlyEventSourceMappingsOfItsOwnFunctions(t *testing.T) {
	r := defaultNamespace.ScopedARNs()
	want := conditionJSON(t, map[string]any{"ArnLike": map[string]any{"lambda:FunctionArn": r.bootstrapFunction}})
	for g := range grantsOf(t, mustRender(t, BootstrapCredentialPermissions)) {
		if !strings.HasSuffix(g.action, "EventSourceMapping") {
			continue
		}
		if g.condition != want {
			t.Errorf("the bootstrap tier grants %s on %s under %s, want it pinned to the bootstrap's own functions through lambda:FunctionArn", g.action, g.resource, g.condition)
		}
	}
}

func TestNoTierCanDeleteThePulumiPassphrase(t *testing.T) {
	r := defaultNamespace.ScopedARNs()
	for tier, document := range bothTiers(t) {
		for g := range grantsOf(t, document) {
			if !strings.HasPrefix(g.action, "ssm:Delete") {
				continue
			}
			if iamResourceMatches(g.resource, r.passphraseParam) {
				t.Errorf("the %s tier grants %s on %s, which reaches %s: deleting the only copy of the passphrase strands every Pulumi stack in the account", tier, g.action, g.resource, r.passphraseParam)
			}
		}
	}
	bootstrapGrants := grantsOf(t, mustRender(t, BootstrapCredentialPermissions))
	for _, resource := range []string{r.edgeParam, r.originParam, r.stackRecord} {
		if !bootstrapGrants[grant{action: "ssm:DeleteParameter", resource: resource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the bootstrap tier cannot delete %s, which a teardown reclaims", resource)
		}
	}
}
