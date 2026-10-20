package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
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
	"cloudfront:CreateCachePolicy",
	"cloudfront:CreateKeyValueStore",
	"cloudfront:CreateOriginAccessControl",
	"cloudfront:CreateResponseHeadersPolicy",
	"cloudfront:DeleteCachePolicy",
	"cloudfront:DeleteKeyValueStore",
	"cloudfront:DeleteOriginAccessControl",
	"cloudfront:DeleteResponseHeadersPolicy",
	"cloudfront:UpdateCachePolicy",
	"cloudfront:UpdateOriginAccessControl",
	"cloudfront:UpdateResponseHeadersPolicy",
	"ecs:DeregisterTaskDefinition",
	"rds-db:connect",
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
	"lambda:DeleteLayerVersion",
}

func parsePolicy(t *testing.T, document string) parsedPolicy {
	t.Helper()
	if strings.HasPrefix(strings.TrimSpace(document), "[") {
		var joined parsedPolicy
		for _, printed := range printedPolicies(t, document) {
			part := parsePolicy(t, string(printed.PolicyDocument))
			joined.Version = part.Version
			joined.Statement = append(joined.Statement, part.Statement...)
		}
		return joined
	}
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
					if slices.ContainsFunc(bothTiers, func(tier environment.Tier) bool {
						scoped := defaultNamespace.Scope(tier)
						return slices.Contains(scoped.BootstrapRoles, resource)
					}) {
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

func conditionJSON(t *testing.T, condition map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(condition)
	if err != nil {
		t.Fatalf("marshal condition: %v", err)
	}
	return string(encoded)
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

func TestEveryManagedByConditionAdmitsTheTagOcelWrites(t *testing.T) {
	writtenByOcel := string(defaultNamespace)
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
							purpose, strings.Join(stringsOf(t, statement.Action, "Action"), ", "), key, operator, pattern, naming.ManagedByTagKey, writtenByOcel,
						)
					}
				}
			}
		}
	}
}

func TestEveryGrantHeldToOcelsTagIsAlsoHeldToItsTier(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		for _, statement := range parsePolicy(t, document).Statement {
			for _, scope := range []string{"aws:RequestTag/", "aws:ResourceTag/"} {
				if conditionNames(statement.Condition, scope+naming.ManagedByTagKey) && !conditionNames(statement.Condition, scope+naming.EnvTierTagKey) {
					t.Errorf(
						"the %s credential grants %s on %s under %s%s alone, which every tier's resources of the namespace carry",
						purpose, strings.Join(stringsOf(t, statement.Action, "Action"), ", "), strings.Join(stringsOf(t, statement.Resource, "Resource"), ", "), scope, naming.ManagedByTagKey,
					)
				}
			}
		}
	}
}

var bothTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func credentialsOfTier(t *testing.T, tier environment.Tier) map[string]string {
	t.Helper()
	bootstrapDoc, deployDoc := renderedCredentialsOf(t, tier)
	return map[string]string{"bootstrap": bootstrapDoc, "deploy": deployDoc}
}

func grantsAction(patterns []string, action string) bool {
	return slices.ContainsFunc(patterns, func(pattern string) bool { return conditionAdmits("StringLike", pattern, action) })
}

func reaches(t *testing.T, document, action, arn string) bool {
	t.Helper()
	for _, statement := range parsePolicy(t, document).Statement {
		if !grantsAction(stringsOf(t, statement.Action, "Action"), action) {
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
	for _, statement := range parsePolicy(t, document).Statement {
		if !grantsAction(stringsOf(t, statement.Action, "Action"), action) {
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

func taggedWithTier(tier environment.Tier, also requestContext) requestContext {
	request := requestContext{"aws:ResourceTag/" + naming.EnvTierTagKey: string(tier)}
	maps.Copy(request, also)
	return request
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

type printedPolicy struct {
	PolicyName     string          `json:"PolicyName"`
	PolicyDocument json.RawMessage `json:"PolicyDocument"`
}

func printedPolicies(t *testing.T, document string) []printedPolicy {
	t.Helper()
	var policies []printedPolicy
	if err := json.Unmarshal([]byte(document), &policies); err != nil {
		t.Fatalf("the printed credential is not a list of managed policies: %v\n%s", err, document)
	}
	return policies
}

func iamCharacters(raw []byte) int {
	n := 0
	for _, r := range string(raw) {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

func TestEveryCredentialFitsTheManagedPoliciesIAMAccepts(t *testing.T) {
	t.Parallel()

	for name, document := range bothCredentials(t) {
		policies := printedPolicies(t, document)
		if len(policies) == 0 {
			t.Errorf("%s prints no policy", name)
			continue
		}
		if len(policies) > iam.ManagedPoliciesPerPrincipal {
			t.Errorf("%s needs %d managed policies, and IAM attaches %d to a role or user by default", name, len(policies), iam.ManagedPoliciesPerPrincipal)
		}
		seen := map[string]bool{}
		for _, policy := range policies {
			if size := iamCharacters(policy.PolicyDocument); size > iam.ManagedPolicyChars {
				t.Errorf("%s policy %s is %d characters, and IAM refuses a managed policy over %d", name, policy.PolicyName, size, iam.ManagedPolicyChars)
			}
			if policy.PolicyName == "" || seen[policy.PolicyName] {
				t.Errorf("%s names a policy %q, want every policy named apart", name, policy.PolicyName)
			}
			seen[policy.PolicyName] = true
			if !strings.HasPrefix(policy.PolicyName, string(defaultNamespace)+"-") {
				t.Errorf("%s names a policy %q outside the namespace", name, policy.PolicyName)
			}
		}
	}
}

var productionScope = defaultNamespace.Scope(environment.TierProduction)
