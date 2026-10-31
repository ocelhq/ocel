package control

import (
	"encoding/json"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func ConnectorStatements(ns account.Namespace, variablesKey string) []iam.Statement {
	var statements []iam.Statement
	seen := map[string]bool{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, statement := range deployStatements(scopeOf(ns, tier, variablesKey)) {
			served := servedBy(statement.Actions)
			if len(served) == 0 {
				continue
			}
			statement.Actions = served
			rendered, err := json.Marshal(iam.Statements([]iam.Statement{statement}))
			if err == nil && seen[string(rendered)] {
				continue
			}
			seen[string(rendered)] = true
			statements = append(statements, statement)
		}
	}
	return statements
}

func servedBy(granted []string) []string {
	var served []string
	for _, action := range servedActions {
		if slices.ContainsFunc(granted, func(pattern string) bool { return matchesAction(pattern, action) }) {
			served = append(served, action)
		}
	}
	return served
}

func matchesAction(pattern, action string) bool {
	matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(action))
	return err == nil && matched
}

func ConnectorPolicies(ns account.Namespace, variablesKey string) ([]iam.ManagedPolicy, error) {
	named := func(i int) string { return ns.PolicyName("connector-" + strconv.Itoa(i)) }
	return iam.Split("connector", named, ConnectorStatements(ns, variablesKey))
}
