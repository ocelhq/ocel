package account

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const witnessWord = "w"

type productionWitness struct {
	action   string
	resource string
	arn      string
	request  requestContext
}

var actionsOnWhatBothTiersShare = []string{
	"iam:CreateServiceLinkedRole",
}

var resourcesAWSOwns = []string{
	bastion.PortForwardingDocumentARN,
}

var resourcesAWSNamesByIDAndScopesByNoTag = []string{
	iam.EdgeRoutesStoreARN,
	iam.EdgeCachePolicyARN,
	iam.EdgeHeadersPolicyARN,
	iam.EdgeAssetAccessARN,
}

var actionsOnCertificatesEitherTierMayAdopt = []string{
	"acm:DescribeCertificate",
}

func carriedByEveryProductionResource() requestContext {
	return requestContext{
		"aws:ResourceTag/" + naming.ManagedByTagKey: string(defaultNamespace),
		"aws:ResourceTag/" + naming.EnvTierTagKey:   string(environment.TierProduction),
		"aws:RequestTag/" + naming.ManagedByTagKey:  string(defaultNamespace),
		"aws:RequestTag/" + naming.EnvTierTagKey:    string(environment.TierProduction),
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

func operandValues(operand any) []string {
	switch v := operand.(type) {
	case string:
		return []string{v}
	case []any:
		var values []string
		for _, each := range v {
			values = append(values, each.(string))
		}
		return values
	}
	return nil
}

func productionRequestSatisfying(t *testing.T, condition map[string]any) requestContext {
	t.Helper()
	request := carriedByEveryProductionResource()
	nulls, _ := condition["Null"].(map[string]any)
	for key, operand := range nulls {
		if operandValues(operand)[0] == "true" {
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
			request[key] = filledIn(t, operandValues(operand)[0], request)
		}
	}
	return request
}

func productionWitnesses(t *testing.T) []productionWitness {
	t.Helper()
	var witnesses []productionWitness
	for _, document := range credentialsOfTier(t, environment.TierProduction) {
		for _, statement := range parsePolicy(t, document).Statement {
			request := productionRequestSatisfying(t, statement.Condition)
			for _, action := range stringsOf(t, statement.Action, "Action") {
				for _, resource := range stringsOf(t, statement.Resource, "Resource") {
					witnesses = append(witnesses, productionWitness{
						action:   filledIn(t, action, request),
						resource: resource,
						arn:      filledIn(t, resource, request),
						request:  maps.Clone(request),
					})
				}
			}
		}
	}
	return witnesses
}

func isOfNoTier(w productionWitness) bool {
	switch {
	case w.resource == iam.AnyResource && readOnly(w.action):
		return true
	case slices.Contains(actionsAWSGivesNoScopingKey, w.action),
		slices.Contains(actionsOnWhatBothTiersShare, w.action),
		slices.Contains(actionsOnCertificatesEitherTierMayAdopt, w.action):
		return true
	}
	return slices.Contains(resourcesAWSOwns, w.resource) || slices.Contains(resourcesAWSNamesByIDAndScopesByNoTag, w.resource)
}

func tagsOnlyInsideACreate(condition map[string]any) bool {
	return conditionNames(condition, "ec2:CreateAction")
}

func allowsTheWitness(t *testing.T, statements []parsedStatement, w productionWitness) bool {
	t.Helper()
	for _, statement := range statements {
		if !grantsAction(stringsOf(t, statement.Action, "Action"), w.action) {
			continue
		}
		if !slices.ContainsFunc(stringsOf(t, statement.Resource, "Resource"), func(pattern string) bool {
			resolved, ok := resolveVariables(t, pattern, w.request)
			return ok && iamResourceMatches(resolved, w.arn)
		}) {
			continue
		}
		if namesAResourceTagAWSIgnoresFor(w.action, statement.Condition) || tagsOnlyInsideACreate(statement.Condition) {
			continue
		}
		if conditionHolds(t, statement.Condition, w.request) {
			return true
		}
	}
	return false
}

func TestAPreviewCredentialReachesNoResourceAProductionCredentialReaches(t *testing.T) {
	t.Parallel()

	witnesses := productionWitnesses(t)
	for purpose, document := range credentialsOfTier(t, environment.TierPreview) {
		statements := parsePolicy(t, document).Statement
		for _, w := range witnesses {
			if !isOfNoTier(w) && allowsTheWitness(t, statements, w) {
				t.Errorf("the preview %s credential can %s on %s, which the production credentials reach as %s", purpose, w.action, w.arn, w.resource)
			}
		}
	}
}
