package deploy

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const rolePolicyToken = "aws:iam/rolePolicy:RolePolicy"

type policyRecorder struct {
	mu       sync.Mutex
	policies map[string]string
}

func (r *policyRecorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if args.TypeToken == rolePolicyToken {
		if r.policies == nil {
			r.policies = map[string]string{}
		}
		policy := ""
		if raw, ok := args.Inputs["policy"]; ok && raw.IsString() {
			policy = raw.StringValue()
		}
		r.policies[args.Name] = policy
	}
	return args.Name + "-id", args.Inputs, nil
}

func (r *policyRecorder) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func (r *policyRecorder) named(fragment string) map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]string{}
	for name, policy := range r.policies {
		if strings.Contains(name, fragment) {
			out[name] = policy
		}
	}
	return out
}

func renderAppRole(t *testing.T, app string, policies []bindingPolicy) *policyRecorder {
	t.Helper()
	rec := &policyRecorder{}
	program := func(pctx *pulumi.Context) error {
		role := appExecutionRole(Config{VarsKeyARN: "arn:aws:kms:us-east-1:1:key/k"}, app, nil, nil, appBundle{}, nil, policies, false, nil)
		_, err := newFunctionRole(pctx, roleCoordinate("shop", testStack(t, "prod", app)), role)
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--"+app, rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}
	return rec
}

const stateTableARN = "arn:aws:dynamodb:us-east-1:1234:table/ocel-state"

var testSessions = newSessionScope("shop", "prod", stateTableARN)

func grantsBindings() []*bindingsv1.Binding {
	return []*bindingsv1.Binding{
		{Name: "bucket--uploads", Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "shop-prod-uploads-abc"}}, Grants: bucketGrants("shop-prod-uploads-abc", testSessions)},
		{Name: "database--main", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "db.host", Port: 5432}}},
	}
}

func plannedBindings(bindings []*bindingsv1.Binding) []providerkit.Binding {
	out := make([]providerkit.Binding, 0, len(bindings))
	for _, binding := range bindings {
		held := providerkit.Binding{Type: providerkit.BindingCustom, Name: binding.GetName(), Grants: providerkit.GrantsOf(binding)}
		switch naming.BindingTypeOf(binding) {
		case bindingsv1.BindingType_BINDING_TYPE_BUCKET:
			held.Type = providerkit.BindingBucket
		case bindingsv1.BindingType_BINDING_TYPE_POSTGRES:
			held.Type = providerkit.BindingPostgres
		}
		out = append(out, held)
	}
	return out
}

func TestBindingPoliciesRenderOnlyForAGrantingBinding(t *testing.T) {
	t.Parallel()

	t.Run("a granting binding carries one inline policy", func(t *testing.T) {
		t.Parallel()

		policies, err := planBindingPolicies(plannedBindings(grantsBindings()))
		if err != nil {
			t.Fatalf("planBindingPolicies: %v", err)
		}
		if len(policies) != 1 || policies[0].Binding != "bucket--uploads" {
			t.Fatalf("policies = %+v, want one for bucket--uploads", policies)
		}

		rendered := renderAppRole(t, "web", policies).named("binding")
		if len(rendered) != 1 {
			t.Fatalf("rendered binding policies = %v, want exactly one", rendered)
		}
		for name, policy := range rendered {
			if !strings.Contains(name, naming.Join(naming.WordSeparator, "binding", "bucket--uploads")) {
				t.Errorf("policy name = %q, want the binding's logical name in it", name)
			}
			if !strings.Contains(policy, "s3:PutObject") || strings.Contains(policy, `"*"`) {
				t.Errorf("policy = %q, want scoped s3 actions", policy)
			}
			var doc struct {
				Statement []struct {
					Resource  []string
					Condition map[string]map[string][]string
				}
			}
			if err := json.Unmarshal([]byte(policy), &doc); err != nil {
				t.Fatalf("unmarshal policy: %v", err)
			}
			for _, s := range doc.Statement {
				for _, r := range s.Resource {
					if strings.Contains(r, "shop-prod-uploads-abc") {
						continue
					}
					if r != stateTableARN {
						t.Errorf("resource = %q, want the bucket this binding names or the table its sessions live in", r)
						continue
					}
					if got := s.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]; !slices.Equal(got, []string{testSessions.KeyPrefix + "*"}) {
						t.Errorf("sessions statement condition = %v, want the app held to this deploy's own session keys", s.Condition)
					}
				}
			}
		}
	})

	t.Run("a grant-free binding carries no policy", func(t *testing.T) {
		t.Parallel()

		policies, err := planBindingPolicies(plannedBindings(grantsBindings()[1:]))
		if err != nil {
			t.Fatalf("planBindingPolicies: %v", err)
		}
		if len(policies) != 0 {
			t.Fatalf("policies = %+v, want none for a grant-free binding", policies)
		}
		if rendered := renderAppRole(t, "admin", policies).named("binding"); len(rendered) != 0 {
			t.Fatalf("rendered binding policies = %v, want none", rendered)
		}
	})
}

func TestUnscopedGrantsAreRejected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		grant *bindingsv1.Grant
	}{
		{"a wildcard resource", &bindingsv1.Grant{Label: "objects", Actions: []string{"s3:GetObject"}, Resources: []string{"*"}}},
		{"a wildcard action", &bindingsv1.Grant{Label: "objects", Actions: []string{"*"}, Resources: []string{"arn:aws:s3:::b/*"}}},
		{"no resource at all", &bindingsv1.Grant{Label: "objects", Actions: []string{"s3:GetObject"}}},
	} {
		t.Run(tc.name+" is rejected", func(t *testing.T) {
			t.Parallel()

			bindings := []*bindingsv1.Binding{{Name: "bucket--uploads", Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "b"}}, Grants: []*bindingsv1.Grant{tc.grant}}}

			var unscoped *UnscopedGrantError
			if err := VerifyGrants(plannedBindings(bindings)[0]); !errors.As(err, &unscoped) {
				t.Fatalf("VerifyGrants = %v, want an *UnscopedGrantError", err)
			}
			if unscoped.Binding != "bucket--uploads" {
				t.Errorf("Binding = %q, want bucket--uploads", unscoped.Binding)
			}
			if _, err := planBindingPolicies(plannedBindings(bindings)); !errors.As(err, &unscoped) {
				t.Fatalf("planBindingPolicies = %v, want an *UnscopedGrantError", err)
			}
		})
	}

	t.Run("scoped grants pass", func(t *testing.T) {
		t.Parallel()

		for _, binding := range plannedBindings(grantsBindings()) {
			if err := VerifyGrants(binding); err != nil {
				t.Fatalf("VerifyGrants(%s) = %v, want nil", binding.Name, err)
			}
		}
	})
}

func TestGrantConditionMergesConditionsOnOneKey(t *testing.T) {
	t.Parallel()

	grant := &bindingsv1.Grant{
		Label:     "sessions",
		Actions:   []string{"dynamodb:GetItem"},
		Resources: []string{stateTableARN},
		Conditions: []*bindingsv1.GrantCondition{
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

	policy, err := bindingPolicyDocument("bucket--uploads", []*bindingsv1.Grant{grant})
	if err != nil {
		t.Fatalf("bindingPolicyDocument: %v", err)
	}
	for _, want := range []string{"SESSION#a*", "SESSION#b*", "SPECIFIC_ATTRIBUTES"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy = %s, missing %q", policy, want)
		}
	}
}
