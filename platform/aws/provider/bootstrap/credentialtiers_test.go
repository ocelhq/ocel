package bootstrap

import (
	"encoding/json"
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
	"aws:ResourceAccount",
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
	"lambda:DeleteLayerVersion",
	"lambda:PublishLayerVersion",
}

var bootstrapOnlyActions = []string{
	"cloudformation:CreateChangeSet",
	"cloudformation:CreateStack",
	"cloudformation:DeleteChangeSet",
	"cloudformation:DeleteStack",
	"cloudformation:ExecuteChangeSet",
	"dynamodb:CreateTable",
	"dynamodb:DeleteTable",
	"iam:CreateAccessKey",
	"iam:CreatePolicy",
	"iam:CreateUser",
	"iam:DeletePolicy",
	"iam:DeleteAccessKey",
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
	bootstrapDoc, err := BootstrapCredentialPolicy()
	if err != nil {
		t.Fatalf("BootstrapCredentialPolicy() error = %v", err)
	}
	deployDoc, err := DeployCredentialPolicy()
	if err != nil {
		t.Fatalf("DeployCredentialPolicy() error = %v", err)
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
	return slices.Equal(arns, []string{appBoundaryARNFor(ClassProduction), appBoundaryARNFor(ClassPreview)})
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
					if resource == substrateRoleARN {
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
	deployActions := actionsOf(t, mustRender(t, DeployCredentialPolicy))
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

func TestOnlyTheEdgeUserIsMintedAndItCarriesNoManagedPolicy(t *testing.T) {
	for tier, document := range bothTiers(t) {
		for g := range grantsOf(t, document) {
			if g.action == "iam:AttachUserPolicy" {
				t.Errorf("the %s tier grants iam:AttachUserPolicy, which turns a minted user into whatever policy it names", tier)
			}
			if !strings.HasPrefix(g.action, "iam:") || !strings.Contains(g.action, "User") && !strings.Contains(g.action, "AccessKey") {
				continue
			}
			if g.resource != edgeUserARN {
				t.Errorf("the %s tier grants %s on %q, which is not the edge user", tier, g.action, g.resource)
			}
		}
	}

	bootstrapActions := actionsOf(t, mustRender(t, BootstrapCredentialPolicy))
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

func mustRender(t *testing.T, render func() (string, error)) string {
	t.Helper()
	document, err := render()
	if err != nil {
		t.Fatalf("render policy: %v", err)
	}
	return document
}
