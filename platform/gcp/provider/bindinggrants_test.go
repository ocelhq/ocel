package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	pubsub "google.golang.org/api/pubsub/v1"
	storagejson "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

const (
	hostAccount = "projects/acme-prod/serviceAccounts/host@acme-prod.iam.gserviceaccount.com"
	tenantRepo  = "projects/acme-prod/locations/europe-west1/repositories/tenants"
	buildTopic  = "projects/acme-prod/topics/builds"
)

func fleetBinding(grants ...provider.Grant) provider.Binding {
	return provider.Binding{Type: provider.BindingCustom, Name: "fleet", Source: "pulumi", Grants: grants}
}

func fleetGrants() []provider.Grant {
	return []provider.Grant{
		{Role: "roles/compute.instanceAdmin.v1", Resource: "projects/acme-prod"},
		{Role: "roles/cloudbuild.builds.editor", Resource: "projects/acme-builds"},
		{Role: "roles/iam.serviceAccountUser", Resource: hostAccount},
		{Role: "roles/artifactregistry.writer", Resource: tenantRepo},
		{Role: "roles/storage.objectViewer", Resource: "projects/_/buckets/tenant-images"},
		{Role: "roles/pubsub.publisher", Resource: buildTopic},
	}
}

func bindingGrantsServer() *googletest.IAM {
	server := appAccountsOnly()
	server.AccountPolicies = map[string]*iam.Policy{
		"/v1/" + hostAccount: {Etag: "BwXhoLA=", Bindings: []*iam.Binding{{Role: "roles/iam.serviceAccountUser", Members: []string{"user:operator@acme.example"}}}},
	}
	server.ProjectPolicies = map[string]*cloudresourcemanager.Policy{
		"acme-builds": {Etag: "BwXhoLA=", Bindings: []*cloudresourcemanager.Binding{{Role: "roles/cloudbuild.builds.editor", Members: []string{"user:operator@acme.example"}}}},
	}
	server.BucketPolicies = map[string]*storagejson.Policy{}
	server.TopicPolicies = map[string]*pubsub.Policy{}
	return server
}

func boundToFleet(grants ...provider.Grant) provider.StackSpec {
	spec := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	spec.App.Grants = []provider.Binding{fleetBinding(grants...)}
	return spec
}

func ensureFleetAccount(t *testing.T, p *Provider, c *gcloud.Clients, spec provider.StackSpec) string {
	t.Helper()
	email, _, err := p.release().EnsureAccount(context.Background(), c, spec, nil)
	if err != nil {
		t.Fatalf("EnsureAccount() = %v", err)
	}
	return "serviceAccount:" + email
}

func heldOn(server *googletest.IAM, member string) []string {
	server.Mu.Lock()
	defer server.Mu.Unlock()
	var held []string
	add := func(on, role string, members []string) {
		if slices.Contains(members, member) {
			held = append(held, role+" on "+on)
		}
	}
	for _, binding := range server.Project.Bindings {
		add("projects/acme-prod", binding.Role, binding.Members)
	}
	for project, policy := range server.ProjectPolicies {
		for _, binding := range policy.Bindings {
			add("projects/"+project, binding.Role, binding.Members)
		}
	}
	for path, policy := range server.AccountPolicies {
		for _, binding := range policy.Bindings {
			add(strings.TrimPrefix(path, "/v1/"), binding.Role, binding.Members)
		}
	}
	for path, policy := range server.RepositoryPolicies {
		for _, binding := range policy.Bindings {
			add(path, binding.Role, binding.Members)
		}
	}
	for bucket, policy := range server.BucketPolicies {
		for _, binding := range policy.Bindings {
			add("projects/_/buckets/"+bucket, binding.Role, binding.Members)
		}
	}
	for path, policy := range server.TopicPolicies {
		for _, binding := range policy.Bindings {
			add(strings.TrimPrefix(path, "/v1/"), binding.Role, binding.Members)
		}
	}
	slices.Sort(held)
	return held
}

func TestAnAppBoundToABindingHoldsEachRoleTheBindingWasPublishedWith(t *testing.T) {
	t.Parallel()
	server := bindingGrantsServer()
	p, c := ensuringAccounts(t, server)

	member := ensureFleetAccount(t, p, c, boundToFleet(fleetGrants()...))

	held := heldOn(server, member)
	for _, grant := range fleetGrants() {
		if want := grant.Role + " on " + grant.Resource; !slices.Contains(held, want) {
			t.Errorf("the app's account holds %v, want %s", held, want)
		}
	}
}

func TestAnAppsBindingGrantsLeaveEveryoneElsesRolesInPlace(t *testing.T) {
	t.Parallel()
	server := bindingGrantsServer()
	p, c := ensuringAccounts(t, server)

	ensureFleetAccount(t, p, c, boundToFleet(fleetGrants()...))

	for _, on := range []string{"projects/acme-builds", hostAccount} {
		if held := heldOn(server, "user:operator@acme.example"); !slices.ContainsFunc(held, func(one string) bool { return strings.HasSuffix(one, " on "+on) }) {
			t.Errorf("the operator holds %v after the deploy, want its role on %s kept", held, on)
		}
	}
}

func TestADeployOfUnchangedBindingGrantsChangesNoPolicy(t *testing.T) {
	t.Parallel()
	server := bindingGrantsServer()
	p, c := ensuringAccounts(t, server)
	spec := boundToFleet(fleetGrants()...)
	ensureFleetAccount(t, p, c, spec)
	writes := func() int {
		server.Mu.Lock()
		defer server.Mu.Unlock()
		return server.Writes + server.AccountWrites + server.RepositoryWrites + server.BucketWrites + server.TopicWrites
	}
	before := writes()

	ensureFleetAccount(t, p, c, spec)

	if after := writes(); after != before {
		t.Errorf("a second deploy of the same grants wrote %d policies, want none", after-before)
	}
}

func TestADeployThatNoLongerBindsAnAppToABindingTakesItsRolesBack(t *testing.T) {
	t.Parallel()
	server := bindingGrantsServer()
	p, c := ensuringAccounts(t, server)
	member := ensureFleetAccount(t, p, c, boundToFleet(fleetGrants()...))

	ensureFleetAccount(t, p, c, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	held := heldOn(server, member)
	for _, grant := range fleetGrants() {
		if gone := grant.Role + " on " + grant.Resource; slices.Contains(held, gone) {
			t.Errorf("the app's account still holds %s after a deploy that no longer binds fleet", gone)
		}
	}
	if operator := heldOn(server, "user:operator@acme.example"); len(operator) != 2 {
		t.Errorf("the operator holds %v after the revoke, want both its roles kept", operator)
	}
}

func TestARemovedAppGivesBackItsBindingGrants(t *testing.T) {
	t.Parallel()
	server := bindingGrantsServer()
	p, c := ensuringAccounts(t, server)
	spec := boundToFleet(fleetGrants()...)
	spec.Ref.Name = stackOf("production", "api", "r1")
	recordApp(t, p, spec.Ref, spec.Ref.Name)
	member := ensureFleetAccount(t, p, c, spec)

	removeStack(t, p, c, spec.Ref)

	if held := heldOn(server, member); len(held) != 0 {
		t.Errorf("the removed app's account still holds %v", held)
	}
}

func TestABindingGrantRefusedByGoogleNamesTheResourceAndThePermissionTheDeployLacks(t *testing.T) {
	t.Parallel()
	server := bindingGrantsServer()
	server.AccountPolicyDenied = true
	p, c := ensuringAccounts(t, server)

	_, _, err := p.release().EnsureAccount(context.Background(), c, boundToFleet(provider.Grant{Role: "roles/iam.serviceAccountUser", Resource: hostAccount}), nil)

	if err == nil || !strings.Contains(err.Error(), hostAccount) || !strings.Contains(err.Error(), "iam.serviceAccounts.setIamPolicy") || !strings.Contains(err.Error(), "fleet") {
		t.Errorf("EnsureAccount() = %v, want it to name fleet, the account and the permission the deploy credential lacks on it", err)
	}
}

func TestABindingGrantGCPCannotHoldIsRefusedBeforeAnythingIsRead(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s before refusing a binding grant", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)
	p := pushing(t, served.URL)

	for _, refused := range []struct {
		grant provider.Grant
		says  string
	}{
		{provider.Grant{Role: "roles/owner", Resource: "projects/acme-prod"}, "basic role"},
		{provider.Grant{Role: "roles/editor", Resource: "projects/acme-prod"}, "basic role"},
		{provider.Grant{Role: "roles/viewer", Resource: "projects/acme-prod"}, "basic role"},
		{provider.Grant{Role: "compute.instances.create", Resource: "projects/acme-prod"}, "role"},
		{provider.Grant{Role: "roles/compute.instanceAdmin.v1", Resource: "projects/acme-prod/zones/us-central1-a/instances/host-1"}, "projects/<id>"},
		{provider.Grant{Role: "roles/storage.objectViewer", Resource: "tenant-images"}, "projects/_/buckets/<name>"},
		{provider.Grant{Actions: []string{"ec2:RunInstances"}, Resources: []string{"arn:aws:ec2:us-east-1:1:instance/*"}}, "role"},
		{provider.Grant{Role: "roles/storage.objectViewer", Resource: "projects/_/buckets/tenant-images", Conditions: []provider.GrantCondition{{Operator: "StringLike", Key: "s3:prefix", Values: []string{"a/*"}}}}, "condition"},
	} {
		pre := provider.DeployPreflight{
			Deploy: provider.DeploySpec{Tier: environment.TierProduction},
			Apps:   []provider.AppUsage{{App: "api", Grants: []provider.Binding{fleetBinding(refused.grant)}}},
		}
		err := p.ApplyPreflight(context.Background(), pre)
		var refusing refusal.Refusal
		if !errors.As(err, &refusing) || refusing.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "fleet") || !strings.Contains(err.Error(), refused.says) {
			t.Errorf("ApplyPreflight(%+v) = %v, want an %s refusal naming fleet and %q", refused.grant, err, refusal.CodeInvalid, refused.says)
		}
		if _, err := p.PlanPreflight(context.Background(), pre); !errors.As(err, &refusing) {
			t.Errorf("PlanPreflight(%+v) = %v, want the grant refused when the deploy is planned too", refused.grant, err)
		}
	}
}

func TestAnAppBoundOnlyToACustomRecordSealsNoProjectValues(t *testing.T) {
	t.Parallel()
	use := newDeployUse(provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: "cloud"},
		Apps:   []provider.AppUsage{{App: "api", Grants: []provider.Binding{fleetBinding(fleetGrants()...)}}},
	})

	if use.sealsProjectValues() {
		t.Error("a deploy whose one binding is a custom record seals project values, and no app is handed a custom record's values")
	}
}
