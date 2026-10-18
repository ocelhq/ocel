package iam

import (
	"errors"
	"slices"
	"strings"
	"testing"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestGrantConditionMergesConditionsOnOneKey(t *testing.T) {
	t.Parallel()

	grant := &variablestorev1.Grant{
		Label:     "sessions",
		Actions:   []string{"dynamodb:GetItem"},
		Resources: []string{"arn:aws:dynamodb:us-east-1:1234:table/ocel-state"},
		Conditions: []*variablestorev1.GrantCondition{
			{Operator: "ForAllValues:StringLike", Key: "dynamodb:LeadingKeys", Values: []string{"SESSION#a*"}},
			{Operator: "ForAllValues:StringLike", Key: "dynamodb:LeadingKeys", Values: []string{"SESSION#b*", "SESSION#a*"}},
			{Operator: "StringEquals", Key: "dynamodb:Select", Values: []string{"SPECIFIC_ATTRIBUTES"}},
		},
	}

	condition := grantCondition(grant)

	leading := condition["ForAllValues:StringLike"].(map[string][]string)["dynamodb:LeadingKeys"]
	if !slices.Equal(leading, []string{"SESSION#a*", "SESSION#b*"}) {
		t.Errorf("dynamodb:LeadingKeys = %v, want both conditions on the key merged and deduplicated", leading)
	}
	if got := condition["StringEquals"].(map[string][]string)["dynamodb:Select"]; !slices.Equal(got, []string{"SPECIFIC_ATTRIBUTES"}) {
		t.Errorf("dynamodb:Select = %v, want the condition under its own operator", got)
	}

	policy, err := GrantPolicy("bucket--uploads", []*variablestorev1.Grant{grant})
	if err != nil {
		t.Fatalf("bindingPolicyDocument: %v", err)
	}
	for _, want := range []string{"SESSION#a*", "SESSION#b*", "SPECIFIC_ATTRIBUTES"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy = %s, missing %q", policy, want)
		}
	}
}

func TestAnUnscopedPublishedGrantIsRefusedBeforeAnyCloudCall(t *testing.T) {
	t.Parallel()

	var unscoped *UnscopedGrantError
	err := VerifyGrants(provider.Binding{
		Name:   "db--main",
		Grants: []provider.Grant{{Label: "everything", Actions: []string{"s3:*"}, Resources: []string{"*"}}},
	})
	if !errors.As(err, &unscoped) {
		t.Fatalf("VerifyGrants = %v, want an *UnscopedGrantError: a publisher may not hand an app blanket access", err)
	}
}
