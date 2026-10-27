package account

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const witnessWord = "w"

type witness struct {
	action   string
	resource string
	arn      string
	request  requestContext
}

var actionsOnWhatBothTiersShare = []string{
	"iam:CreateServiceLinkedRole",
	"iam:AddClientIDToOpenIDConnectProvider",
	"iam:CreateOpenIDConnectProvider",
	"iam:DeleteOpenIDConnectProvider",
	"iam:GetOpenIDConnectProvider",
	"iam:RemoveClientIDFromOpenIDConnectProvider",
	"iam:TagOpenIDConnectProvider",
}

var resourcesAWSOwns = []string{
	bastion.PortForwardingDocumentARN,
	component.VPCQuota.ARN(),
	component.ElasticIPQuota.ARN(),
}

var resourcesAWSNamesByIDAndScopesByNoTag = []string{
	iam.EdgeRoutesStoreARN,
	iam.EdgeCachePolicyARN,
	iam.EdgeHeadersPolicyARN,
	iam.EdgeAssetAccessARN,
}

func requestTaggedFor(tier environment.Tier) requestContext {
	return requestContext{
		"aws:ResourceTag/" + naming.ManagedByTagKey: string(defaultNamespace),
		"aws:ResourceTag/" + naming.EnvTierTagKey:   string(tier),
		"aws:RequestTag/" + naming.ManagedByTagKey:  string(defaultNamespace),
		"aws:RequestTag/" + naming.EnvTierTagKey:    string(tier),
		"aws:PrincipalAccount":                      "111122223333",
		"aws:ResourceAccount":                       "111122223333",
		"aws:PrincipalTag/ocel:project":             "shop",
	}
}

func filledIn(t *testing.T, pattern string, request requestContext) string {
	t.Helper()
	resolved, _ := resolveVariables(t, pattern, request)
	return strings.NewReplacer("*", witnessWord, "?", witnessWord).Replace(resolved)
}

func requestSatisfying(t *testing.T, tier environment.Tier, condition map[string]any) requestContext {
	t.Helper()
	request := requestTaggedFor(tier)
	nulls, _ := condition["Null"].(map[string]any)
	for key, operand := range nulls {
		if operandValues(t, operand)[0] == "true" {
			delete(request, key)
		} else if _, present := request[key]; !present {
			request[key] = witnessWord
		}
	}
	for operator, operands := range condition {
		if operator == "Null" {
			continue
		}
		for key, operand := range operands.(map[string]any) {
			request[key] = filledIn(t, operandValues(t, operand)[0], request)
		}
	}
	return request
}

func tagsOnlyInsideACreate(condition map[string]any) bool {
	return conditionNames(condition, "ec2:CreateAction")
}

func witnessesOf(t *testing.T, tier environment.Tier) []witness {
	t.Helper()
	var witnesses []witness
	for _, document := range credentialsOfTier(t, tier) {
		for _, statement := range parsePolicy(t, document).Statement {
			request := requestSatisfying(t, tier, statement.Condition)
			for _, action := range stringsOf(t, statement.Action, "Action") {
				for _, resource := range stringsOf(t, statement.Resource, "Resource") {
					w := witness{
						action:   filledIn(t, action, request),
						resource: resource,
						arn:      filledIn(t, resource, request),
						request:  maps.Clone(request),
					}
					if !isOfNoTier(w) {
						witnesses = append(witnesses, w)
					}
				}
			}
		}
	}
	return witnesses
}

func isOfNoTier(w witness) bool {
	switch {
	case w.resource == iam.AnyResource && readOnly(w.action):
		return true
	case slices.Contains(actionsAWSGivesNoScopingKey, w.action),
		slices.Contains(actionsOnWhatBothTiersShare, w.action):
		return true
	}
	return slices.Contains(resourcesAWSOwns, w.resource) || slices.Contains(resourcesAWSNamesByIDAndScopesByNoTag, w.resource)
}

func statementsOfTier(t *testing.T, tier environment.Tier) []parsedStatement {
	t.Helper()
	var statements []parsedStatement
	for _, document := range credentialsOfTier(t, tier) {
		statements = append(statements, parsePolicy(t, document).Statement...)
	}
	return statements
}

func TestACredentialReachesNoResourceTheOtherTiersCredentialsReach(t *testing.T) {
	t.Parallel()

	for _, tier := range bothTiers {
		own := statementsOfTier(t, tier)
		witnesses := witnessesOf(t, tier)
		for _, w := range witnesses {
			if !anyStatementAllows(t, own, w.action, w.arn, w.request) {
				t.Fatalf("no %s credential can %s on %s, the request its own grant on %s was built to satisfy", tier, w.action, w.arn, w.resource)
			}
		}
		sibling := tier.Sibling()
		for purpose, document := range credentialsOfTier(t, sibling) {
			statements := slices.DeleteFunc(parsePolicy(t, document).Statement, func(statement parsedStatement) bool {
				return tagsOnlyInsideACreate(statement.Condition)
			})
			for _, w := range witnesses {
				if anyStatementAllows(t, statements, w.action, w.arn, w.request) {
					t.Errorf("the %s %s credential can %s on %s, which the %s credentials reach as %s", sibling, purpose, w.action, w.arn, tier, w.resource)
				}
			}
		}
	}
}
