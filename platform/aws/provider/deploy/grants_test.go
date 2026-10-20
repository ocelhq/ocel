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

	"github.com/ocelhq/ocel/pkg/bindings"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
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

func renderAppRole(t *testing.T, app string, policies []iam.BindingPolicy) *policyRecorder {
	t.Helper()
	rec := &policyRecorder{}
	program := func(pctx *pulumi.Context) error {
		role := appExecutionRole(Config{Namespace: testNamespace, VariablesKeyARN: "arn:aws:kms:us-east-1:1:key/k", AppBoundaryARN: componenttest.BoundaryARN}, app, nil, nil, variables.Bundle{}, nil, policies, false, nil)
		_, err := function.NewRole(pctx, component.AppRoleCoordinate("shop", testStack(t, "prod", app)), role)
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--"+app, rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}
	return rec
}

const stateTableARN = "arn:aws:dynamodb:us-east-1:1234:table/ocel-state"

var testSessions = bucket.NewSessions("shop", "prod", stateTableARN)

func grantsBindings() []*variablestorev1.PublishedBinding {
	return []*variablestorev1.PublishedBinding{
		{Binding: &bindingsv1.Binding{Name: "bucket--uploads", Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "shop-prod-uploads-abc"}}}, Grants: bucket.Grants("shop-prod-uploads-abc", testSessions)},
		{Binding: &bindingsv1.Binding{Name: "database--main", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "db.host", Port: 5432}}}},
	}
}

func plannedBindings(published []*variablestorev1.PublishedBinding) []provider.Binding {
	out := make([]provider.Binding, 0, len(published))
	for _, record := range published {
		binding := record.GetBinding()
		planned := provider.Binding{Type: provider.BindingCustom, Name: binding.GetName(), Grants: provider.GrantsOf(record.GetGrants())}
		switch bindings.TypeOf(binding) {
		case bindingsv1.BindingType_BINDING_TYPE_BUCKET:
			planned.Type = provider.BindingBucket
		case bindingsv1.BindingType_BINDING_TYPE_POSTGRES:
			planned.Type = provider.BindingPostgres
		}
		out = append(out, planned)
	}
	return out
}

func TestBindingPoliciesRenderOnlyForAGrantingBinding(t *testing.T) {
	t.Parallel()

	t.Run("a granting binding gets one inline policy", func(t *testing.T) {
		t.Parallel()

		policies, err := iam.NewBindingPolicies(plannedBindings(grantsBindings()))
		if err != nil {
			t.Fatalf("NewBindingPolicies: %v", err)
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
						t.Errorf("sessions statement condition = %v, want the app limited to this deploy's own session keys", s.Condition)
					}
				}
			}
		}
	})

	t.Run("a grant-free binding gets no policy", func(t *testing.T) {
		t.Parallel()

		policies, err := iam.NewBindingPolicies(plannedBindings(grantsBindings()[1:]))
		if err != nil {
			t.Fatalf("NewBindingPolicies: %v", err)
		}
		if len(policies) != 0 {
			t.Fatalf("policies = %+v, want none for a grant-free binding", policies)
		}
		if rendered := renderAppRole(t, "admin", policies).named("binding"); len(rendered) != 0 {
			t.Fatalf("rendered binding policies = %v, want none", rendered)
		}
	})
}

func TestARecordGrantedUnderTwoDeclaredNamesGetsOneRolePolicy(t *testing.T) {
	t.Parallel()

	granted := plannedBindings(grantsBindings()[:1])
	main, reports := granted[0], granted[0]
	main.Resource, reports.Resource = "main", "reports"

	policies, err := iam.NewBindingPolicies([]provider.Binding{main, reports})
	if err != nil {
		t.Fatalf("NewBindingPolicies: %v", err)
	}
	if len(policies) != 1 || policies[0].Binding != "bucket--uploads" {
		t.Fatalf("policies = %+v, want one for bucket--uploads: both grants carry the same record's grants", policies)
	}
	if rendered := renderAppRole(t, "web", policies).named("binding"); len(rendered) != 1 {
		t.Fatalf("rendered binding policies = %v, want exactly one", rendered)
	}
}

func TestUnscopedGrantsAreRejected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		grant *variablestorev1.Grant
	}{
		{"a wildcard resource", &variablestorev1.Grant{Label: "objects", Actions: []string{"s3:GetObject"}, Resources: []string{"*"}}},
		{"a wildcard action", &variablestorev1.Grant{Label: "objects", Actions: []string{"*"}, Resources: []string{"arn:aws:s3:::b/*"}}},
		{"no resource at all", &variablestorev1.Grant{Label: "objects", Actions: []string{"s3:GetObject"}}},
	} {
		t.Run(tc.name+" is rejected", func(t *testing.T) {
			t.Parallel()

			bindings := []*variablestorev1.PublishedBinding{{Binding: &bindingsv1.Binding{Name: "bucket--uploads", Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "b"}}}, Grants: []*variablestorev1.Grant{tc.grant}}}

			var unscoped *iam.UnscopedGrantError
			if err := iam.VerifyGrants(plannedBindings(bindings)[0]); !errors.As(err, &unscoped) {
				t.Fatalf("iam.VerifyGrants = %v, want an *iam.UnscopedGrantError", err)
			}
			if unscoped.Binding != "bucket--uploads" {
				t.Errorf("Binding = %q, want bucket--uploads", unscoped.Binding)
			}
			if _, err := iam.NewBindingPolicies(plannedBindings(bindings)); !errors.As(err, &unscoped) {
				t.Fatalf("NewBindingPolicies = %v, want an *iam.UnscopedGrantError", err)
			}
		})
	}

	t.Run("scoped grants pass", func(t *testing.T) {
		t.Parallel()

		for _, binding := range plannedBindings(grantsBindings()) {
			if err := iam.VerifyGrants(binding); err != nil {
				t.Fatalf("iam.VerifyGrants(%s) = %v, want nil", binding.Name, err)
			}
		}
	})
}
