package bootstrap

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"unicode"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	managedPolicyChars          = 6144
	managedPoliciesPerPrincipal = 10

	policyVersion = "2012-10-17"
)

type managedPolicy struct {
	PolicyName     string         `json:"PolicyName"`
	PolicyDocument map[string]any `json:"PolicyDocument"`
}

type sizedStatement struct {
	at        int
	statement map[string]any
	chars     int
}

func credentialPolicies(ns Namespace, purpose string, tier environment.Tier, grants []GrantStatement) (string, error) {
	empty, err := iamChars(map[string]any{"Version": policyVersion, "Statement": []any{}})
	if err != nil {
		return "", err
	}
	statements := PolicyStatements(grants)
	sized := make([]sizedStatement, 0, len(statements))
	for i, statement := range statements {
		chars, err := iamChars(statement)
		if err != nil {
			return "", fmt.Errorf("render the %s credential: %w", purpose, err)
		}
		if empty+chars > managedPolicyChars {
			return "", fmt.Errorf("the %s credential has a statement of %d characters, over the %d one managed policy holds", purpose, chars, managedPolicyChars)
		}
		sized = append(sized, sizedStatement{at: i, statement: statement, chars: chars})
	}
	slices.SortStableFunc(sized, func(a, b sizedStatement) int { return cmp.Compare(b.chars, a.chars) })

	var bins [][]sizedStatement
	var filled []int
	for _, statement := range sized {
		placed := false
		for i := range bins {
			if filled[i]+1+statement.chars <= managedPolicyChars {
				bins[i] = append(bins[i], statement)
				filled[i] += 1 + statement.chars
				placed = true
				break
			}
		}
		if !placed {
			bins = append(bins, []sizedStatement{statement})
			filled = append(filled, empty+statement.chars)
		}
	}
	if len(bins) > managedPoliciesPerPrincipal {
		return "", fmt.Errorf("the %s credential needs %d managed policies, over the %d IAM attaches to one role or user by default", purpose, len(bins), managedPoliciesPerPrincipal)
	}

	policies := make([]managedPolicy, 0, len(bins))
	for i, bin := range bins {
		slices.SortFunc(bin, func(a, b sizedStatement) int { return cmp.Compare(a.at, b.at) })
		statements := make([]map[string]any, 0, len(bin))
		for _, statement := range bin {
			statements = append(statements, statement.statement)
		}
		policies = append(policies, managedPolicy{
			PolicyName:     ns.PolicyName(fmt.Sprintf("%s-%s-%d", purpose, tier, i+1)),
			PolicyDocument: map[string]any{"Version": policyVersion, "Statement": statements},
		})
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(policies); err != nil {
		return "", fmt.Errorf("render the %s credential: %w", purpose, err)
	}
	return string(bytes.TrimRight(out.Bytes(), "\n")), nil
}

func iamChars(value any) (int, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range out.String() {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n, nil
}
