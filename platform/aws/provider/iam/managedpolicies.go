package iam

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"unicode"
)

const (
	ManagedPolicyChars          = 6144
	ManagedPoliciesPerPrincipal = 10
)

type ManagedPolicy struct {
	PolicyName     string         `json:"PolicyName"`
	PolicyDocument map[string]any `json:"PolicyDocument"`
}

type sizedStatement struct {
	at        int
	statement map[string]any
	chars     int
}

func Split(purpose string, name func(i int) string, statements []Statement) ([]ManagedPolicy, error) {
	empty, err := Chars(map[string]any{"Version": PolicyVersion, "Statement": []any{}})
	if err != nil {
		return nil, err
	}
	rendered := Statements(statements)
	sized := make([]sizedStatement, 0, len(rendered))
	for i, statement := range rendered {
		chars, err := Chars(statement)
		if err != nil {
			return nil, fmt.Errorf("render the %s credential: %w", purpose, err)
		}
		if empty+chars > ManagedPolicyChars {
			return nil, fmt.Errorf("the %s credential has a statement of %d characters, over the %d one managed policy holds", purpose, chars, ManagedPolicyChars)
		}
		sized = append(sized, sizedStatement{at: i, statement: statement, chars: chars})
	}
	slices.SortStableFunc(sized, func(a, b sizedStatement) int { return cmp.Compare(b.chars, a.chars) })

	var bins [][]sizedStatement
	var filled []int
	for _, statement := range sized {
		placed := false
		for i := range bins {
			if filled[i]+1+statement.chars <= ManagedPolicyChars {
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
	if len(bins) > ManagedPoliciesPerPrincipal {
		return nil, fmt.Errorf("the %s credential needs %d managed policies, over the %d IAM attaches to one role or user by default", purpose, len(bins), ManagedPoliciesPerPrincipal)
	}

	policies := make([]ManagedPolicy, 0, len(bins))
	for i, bin := range bins {
		slices.SortFunc(bin, func(a, b sizedStatement) int { return cmp.Compare(a.at, b.at) })
		statements := make([]map[string]any, 0, len(bin))
		for _, statement := range bin {
			statements = append(statements, statement.statement)
		}
		policies = append(policies, ManagedPolicy{
			PolicyName:     name(i + 1),
			PolicyDocument: map[string]any{"Version": PolicyVersion, "Statement": statements},
		})
	}
	return policies, nil
}

func RenderManagedPolicies(purpose string, policies []ManagedPolicy) (string, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(policies); err != nil {
		return "", fmt.Errorf("render the %s credential: %w", purpose, err)
	}
	return string(bytes.TrimRight(out.Bytes(), "\n")), nil
}

func Chars(value any) (int, error) {
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
