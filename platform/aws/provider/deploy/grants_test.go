package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
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

func renderAppRole(t *testing.T, app string, policies []linkPolicy) *policyRecorder {
	t.Helper()
	rec := &policyRecorder{}
	program := func(pctx *pulumi.Context) error {
		role := appExecutionRole(Config{VarsKeyARN: "arn:aws:kms:us-east-1:1:key/k"}, app, nil, nil, appBundle{}, nil, policies, false)
		_, err := newFunctionRole(pctx, roleCoordinate("shop", testStack(t, "prod", app)), role)
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--"+app, rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}
	return rec
}

func grantsManifest() *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug: "shop",
		Apps: []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "admin"}},
		Resources: []*deploymentsv1.ManifestResource{
			{
				LogicalName: "bucket--uploads",
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: "uploads"},
				Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			},
			{
				LogicalName: "database--main",
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "main"},
				Config:      &deploymentsv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			},
		},
		Usages: []*deploymentsv1.ManifestUsage{
			{App: "web", Resource: "bucket--uploads"},
			{App: "admin", Resource: "database--main"},
		},
	}
}

const stateTableARN = "arn:aws:dynamodb:us-east-1:1234:table/ocel-state"

var testSessions = newSessionScope("shop", "prod", stateTableARN)

func grantsLinks() []*linksv1.Link {
	return []*linksv1.Link{
		{Name: "bucket--uploads", Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "shop-prod-uploads-abc"}}, Grants: bucketGrants("shop-prod-uploads-abc", testSessions)},
		{Name: "database--main", Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "db.host", Port: 5432}}},
	}
}

func TestAppLinkPoliciesRenderOnlyOnTheUsingRole(t *testing.T) {
	t.Parallel()

	manifest, links := grantsManifest(), grantsLinks()

	t.Run("the using app carries one inline policy for the link it uses", func(t *testing.T) {
		t.Parallel()

		policies, err := appLinkPolicies(manifest, "web", links)
		if err != nil {
			t.Fatalf("appLinkPolicies: %v", err)
		}
		if len(policies) != 1 || policies[0].Link != "bucket--uploads" {
			t.Fatalf("policies = %+v, want one for bucket--uploads", policies)
		}

		rendered := renderAppRole(t, "web", policies).named("link")
		if len(rendered) != 1 {
			t.Fatalf("rendered link policies = %v, want exactly one", rendered)
		}
		for name, policy := range rendered {
			if !strings.Contains(name, naming.Join(naming.WordSeparator, "link", "bucket--uploads")) {
				t.Errorf("policy name = %q, want the link's logical name in it", name)
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
						t.Errorf("resource = %q, want the bucket this link names or the table its sessions live in", r)
						continue
					}
					if got := s.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]; !slices.Equal(got, []string{testSessions.KeyPrefix + "*"}) {
						t.Errorf("sessions statement condition = %v, want the app held to this deploy's own session keys", s.Condition)
					}
				}
			}
		}
	})

	t.Run("an app that uses no granting link carries no link policy", func(t *testing.T) {
		t.Parallel()

		policies, err := appLinkPolicies(manifest, "admin", links)
		if err != nil {
			t.Fatalf("appLinkPolicies: %v", err)
		}
		if len(policies) != 0 {
			t.Fatalf("policies = %+v, want none for a grant-free link", policies)
		}
		if rendered := renderAppRole(t, "admin", policies).named("link"); len(rendered) != 0 {
			t.Fatalf("rendered link policies = %v, want none", rendered)
		}
	})

	t.Run("a link the app does not use never reaches its role", func(t *testing.T) {
		t.Parallel()

		unused := append(grantsLinks(), &linksv1.Link{
			Name:       "bucket--reports",
			Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "shop-prod-reports-def"}},
			Grants:     bucketGrants("shop-prod-reports-def", testSessions),
		})
		policies, err := appLinkPolicies(manifest, "web", unused)
		if err != nil {
			t.Fatalf("appLinkPolicies: %v", err)
		}
		for _, p := range policies {
			if p.Link == "bucket--reports" {
				t.Fatalf("policies = %+v, want no policy for an unused link", policies)
			}
		}
	})
}

func TestUnscopedGrantsAreRejected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		grant *linksv1.Grant
	}{
		{"a wildcard resource", &linksv1.Grant{Label: "objects", Actions: []string{"s3:GetObject"}, Resources: []string{"*"}}},
		{"a wildcard action", &linksv1.Grant{Label: "objects", Actions: []string{"*"}, Resources: []string{"arn:aws:s3:::b/*"}}},
		{"no resource at all", &linksv1.Grant{Label: "objects", Actions: []string{"s3:GetObject"}}},
	} {
		t.Run(tc.name+" is rejected", func(t *testing.T) {
			t.Parallel()

			links := []*linksv1.Link{{Name: "bucket--uploads", Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "b"}}, Grants: []*linksv1.Grant{tc.grant}}}

			var unscoped *UnscopedGrantError
			if err := checkLinkGrants(links); !errors.As(err, &unscoped) {
				t.Fatalf("checkLinkGrants = %v, want an *UnscopedGrantError", err)
			}
			if unscoped.Link != "bucket--uploads" {
				t.Errorf("Link = %q, want bucket--uploads", unscoped.Link)
			}
			if _, err := appLinkPolicies(grantsManifest(), "web", links); !errors.As(err, &unscoped) {
				t.Fatalf("appLinkPolicies = %v, want an *UnscopedGrantError", err)
			}
		})
	}

	t.Run("scoped grants pass", func(t *testing.T) {
		t.Parallel()

		if err := checkLinkGrants(grantsLinks()); err != nil {
			t.Fatalf("checkLinkGrants = %v, want nil", err)
		}
	})
}

func TestInlinePolicyBudgetPreflight(t *testing.T) {
	t.Parallel()

	t.Run("a modest app passes", func(t *testing.T) {
		t.Parallel()

		if err := checkInlinePolicyBudget(grantsManifest(), nil, testSessions); err != nil {
			t.Fatalf("checkInlinePolicyBudget = %v, want nil", err)
		}
	})

	t.Run("grant-free links cost nothing", func(t *testing.T) {
		t.Parallel()

		manifest := grantsManifest()
		for i := range 500 {
			name := "database--" + strings.Repeat("d", 40) + string(rune('a'+i%26)) + string(rune('a'+i/26))
			manifest.Resources = append(manifest.Resources, &deploymentsv1.ManifestResource{
				LogicalName: name,
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: name},
				Config:      &deploymentsv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			})
			manifest.Usages = append(manifest.Usages, &deploymentsv1.ManifestUsage{App: "web", Resource: name})
		}
		if err := checkInlinePolicyBudget(manifest, nil, testSessions); err != nil {
			t.Fatalf("checkInlinePolicyBudget = %v, want nil for grant-free links", err)
		}
	})

	t.Run("an over-linked app fails with an itemized bill", func(t *testing.T) {
		t.Parallel()

		manifest := grantsManifest()
		for i := range 60 {
			name := "bucket--" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			manifest.Resources = append(manifest.Resources, &deploymentsv1.ManifestResource{
				LogicalName: name,
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: name},
				Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			})
			manifest.Usages = append(manifest.Usages, &deploymentsv1.ManifestUsage{App: "web", Resource: name})
		}

		var budget *PolicyBudgetError
		err := checkInlinePolicyBudget(manifest, nil, testSessions)
		if !errors.As(err, &budget) {
			t.Fatalf("checkInlinePolicyBudget = %v, want a *PolicyBudgetError", err)
		}
		if len(budget.Apps) != 1 || budget.Apps[0].App != "web" {
			t.Fatalf("Apps = %+v, want web alone", budget.Apps)
		}
		if len(budget.Apps[0].Items) != 61 {
			t.Errorf("Items = %d, want the 61 granting links billed", len(budget.Apps[0].Items))
		}
		message := budget.Error()
		for _, want := range []string{"web", "bucket--uploads", "characters"} {
			if !strings.Contains(message, want) {
				t.Errorf("Error() = %q, missing %q", message, want)
			}
		}
		for _, unwanted := range []string{"quota increase", "Service Quotas", "request an increase"} {
			if strings.Contains(message, unwanted) {
				t.Errorf("Error() = %q, must not suggest %q", message, unwanted)
			}
		}
		items := budget.Apps[0].Items
		for i := 1; i < len(items); i++ {
			if items[i-1].Chars < items[i].Chars {
				t.Fatalf("items are not ordered by cost: %+v", items)
			}
		}
	})

	t.Run("every over-budget app is reported at once", func(t *testing.T) {
		t.Parallel()

		manifest := grantsManifest()
		for i := range 60 {
			name := "bucket--" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			manifest.Resources = append(manifest.Resources, &deploymentsv1.ManifestResource{
				LogicalName: name,
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: name},
				Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			})
			manifest.Usages = append(manifest.Usages,
				&deploymentsv1.ManifestUsage{App: "web", Resource: name},
				&deploymentsv1.ManifestUsage{App: "admin", Resource: name},
			)
		}

		var budget *PolicyBudgetError
		if err := checkInlinePolicyBudget(manifest, nil, testSessions); !errors.As(err, &budget) {
			t.Fatalf("checkInlinePolicyBudget = %v, want a *PolicyBudgetError", err)
		}
		if len(budget.Apps) != 2 || budget.Apps[0].App != "admin" || budget.Apps[1].App != "web" {
			t.Fatalf("Apps = %+v, want admin then web", budget.Apps)
		}
		message := budget.Error()
		for _, want := range []string{"app admin", "app web", "bucket--uploads"} {
			if !strings.Contains(message, want) {
				t.Errorf("Error() = %q, missing %q", message, want)
			}
		}
	})

	t.Run("a repeated usage row is billed once", func(t *testing.T) {
		t.Parallel()

		manifest := grantsManifest()
		manifest.Usages = append(manifest.Usages, &deploymentsv1.ManifestUsage{App: "web", Resource: "bucket--uploads"})
		for i := range 8 {
			name := "bucket--" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			manifest.Resources = append(manifest.Resources, &deploymentsv1.ManifestResource{
				LogicalName: name,
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: name},
				Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			})
			manifest.Usages = append(manifest.Usages,
				&deploymentsv1.ManifestUsage{App: "web", Resource: name},
				&deploymentsv1.ManifestUsage{App: "web", Resource: name},
			)
		}

		policies, err := appLinkPolicies(manifest, "web", grantsLinks())
		if err != nil {
			t.Fatalf("appLinkPolicies: %v", err)
		}
		if len(policies) != 1 {
			t.Fatalf("policies = %+v, want the duplicated link rendered once", policies)
		}
		if err := checkInlinePolicyBudget(manifest, nil, testSessions); err != nil {
			t.Fatalf("checkInlinePolicyBudget = %v, want duplicated usages billed once", err)
		}
	})

	t.Run("the deploy stops before it touches the cloud", func(t *testing.T) {
		t.Parallel()

		manifest := grantsManifest()
		for i := range 60 {
			name := "bucket--" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			manifest.Resources = append(manifest.Resources, &deploymentsv1.ManifestResource{
				LogicalName: name,
				Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_BUCKET, Name: name},
				Config:      &deploymentsv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			})
			manifest.Usages = append(manifest.Usages, &deploymentsv1.ManifestUsage{App: "web", Resource: name})
		}

		fake := &recordingRootStack{}
		_, err := Run(context.Background(), Config{
			Edge:             fake,
			StoreEndpoint:    fakeStoreEndpoint,
			Class:            deploymentsv1.Environment_CLASS_PRODUCTION,
			StateTableARN:    stateTableARN,
			ListenerCodePath: "dist/ocel-listener.zip",
		}, manifest, nil, func(string) {})

		var budget *PolicyBudgetError
		if !errors.As(err, &budget) {
			t.Fatalf("Run = %v, want a *PolicyBudgetError", err)
		}
		if len(fake.reconciles) != 0 || len(fake.historyPointers) != 0 || len(fake.staged) != 0 {
			t.Fatalf("the pre-flight reached the cloud: %+v", fake)
		}
	})

	t.Run("the reserve covers the platform's own policies at the fullest role the budget admits", func(t *testing.T) {
		t.Parallel()

		worst, err := billedResourcePolicy(grantsManifest().GetResources()[0], nil, testSessions)
		if err != nil {
			t.Fatalf("billedResourcePolicy: %v", err)
		}

		links := make([]string, 0, policyBudgetChars/len(worst))
		for i := range cap(links) {
			links = append(links, strings.Repeat("l", maxS3BucketNameLen-2)+string(rune('a'+i%26))+string(rune('a'+i/26)))
		}
		longest := strings.Repeat("n", maxS3BucketNameLen)

		varsPolicy, err := varsReadPolicy(executionRole{
			VarsKeyARN:   productionVarsKeyARN,
			VarsTableARN: varsTableARN,
			Slug:         longest,
			VarsClass:    varsClass,
			VarsLinks:    links,
		})
		if err != nil {
			t.Fatalf("varsReadPolicy: %v", err)
		}
		isr, err := isrPolicy(isrConfig{
			Coord:    naming.Coordinate{Project: longest, Env: longest, App: longest, Kind: naming.KindFunction, Release: fixedRelease(t)},
			Bucket:   longest,
			Prefix:   longest,
			TableARN: varsTableARN,
		})
		if err != nil {
			t.Fatalf("isrPolicy: %v", err)
		}
		bytecode, err := bytecodePolicy(bytecodeConfig{Bucket: longest, Prefix: longest})
		if err != nil {
			t.Fatalf("bytecodePolicy: %v", err)
		}

		total := len(varsPolicy) + len(isr) + len(bytecode)
		if total > platformPolicyReserveChars {
			t.Fatalf(
				"the platform's own inline policies come to %d characters on a role carrying %d links, over the %d reserved below the %d ceiling",
				total, len(links), platformPolicyReserveChars, rolePolicyCeilingChars,
			)
		}
	})

	t.Run("the worst case bounds what a link actually renders", func(t *testing.T) {
		t.Parallel()

		policies, err := appLinkPolicies(grantsManifest(), "web", grantsLinks())
		if err != nil {
			t.Fatalf("appLinkPolicies: %v", err)
		}
		worst, err := billedResourcePolicy(grantsManifest().GetResources()[0], nil, testSessions)
		if err != nil {
			t.Fatalf("billedResourcePolicy: %v", err)
		}
		if len(worst) < len(policies[0].Policy) {
			t.Fatalf("worst case is %d characters, under the %d actually rendered", len(worst), len(policies[0].Policy))
		}
	})
}

func TestGrantConditionMergesConditionsOnOneKey(t *testing.T) {
	t.Parallel()

	grant := &linksv1.Grant{
		Label:     "sessions",
		Actions:   []string{"dynamodb:GetItem"},
		Resources: []string{stateTableARN},
		Conditions: []*linksv1.GrantCondition{
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

	policy, err := linkPolicyDocument("bucket--uploads", []*linksv1.Grant{grant})
	if err != nil {
		t.Fatalf("linkPolicyDocument: %v", err)
	}
	for _, want := range []string{"SESSION#a*", "SESSION#b*", "SPECIFIC_ATTRIBUTES"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy = %s, missing %q", policy, want)
		}
	}
}
