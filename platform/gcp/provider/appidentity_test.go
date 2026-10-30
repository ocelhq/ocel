package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	manifest "github.com/ocelhq/ocel/platform/gcp/provider/resources/variables/live"
)

const (
	ownedAccount  = "web-runtime@acme-prod.iam.gserviceaccount.com"
	ownedPolicy   = "/v1/projects/-/serviceAccounts/" + ownedAccount
	ownedMember   = "serviceAccount:" + ownedAccount
	tasksAgent    = "serviceAccount:service-123456789@gcp-sa-cloudtasks.iam.gserviceaccount.com"
	otherAccount  = "jobs-runtime@acme-prod.iam.gserviceaccount.com"
	otherIdentity = "serviceAccount:" + otherAccount
)

type patchingEach map[string]transform.Patches

func (p patchingEach) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	results := make([]transform.Result, len(req.Resources))
	for i, resource := range req.Resources {
		results[i] = transform.Result{Patches: overTheWire(p[resource.Name])}
	}
	return results, nil
}

func recordStackFunctions(t *testing.T, p *Provider, spec provider.StackSpec, functions []provider.Function) {
	t.Helper()
	if err := stackrecords.Write(context.Background(), p.KeyValues(), spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name,
		stackrecords.Stack{Kind: provider.StackApp, App: spec.Ref.Name.App, Functions: functions}); err != nil {
		t.Fatal(err)
	}
}

func runningAs(account string) *patching {
	return servicePatched(map[string]any{"template": map[string]any{"serviceAccount": account}})
}

func ensureRunningAs(t *testing.T, p *Provider, c *gcloud.Clients, spec provider.StackSpec, pass transform.Pass, declared map[string]*provider.TopicSpec) (string, error) {
	t.Helper()
	p.transforms = pass
	email, _, err := ensureAccount(p, evaluatedFor(t, p, spec), c, spec, declared)
	return email, err
}

func ownAccountHolding(bindings ...*iam.Binding) map[string]*iam.Policy {
	return map[string]*iam.Policy{ownedPolicy: {Etag: "BwXhoLA=", Bindings: bindings}}
}

func TestAnAppPatchedToRunAsAnAccountOfItsOwnRunsAsItAndOcelMakesNoAccount(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	p := servingTransformed(t, server, runningAs(ownedAccount))
	spec := functionStackDeclaring(environment.TierProduction, "production", openingASecret)

	if err := provisionTransformedFunctions(t, p, spec); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	for _, service := range releasedTemplates(server) {
		if service.Template.ServiceAccount != ownedAccount {
			t.Errorf("a service runs as %q, want the patch's %s", service.Template.ServiceAccount, ownedAccount)
		}
	}
	if len(server.Iam.Created) != 0 {
		t.Errorf("created the accounts %+v, want none: the app runs as an account its own infrastructure owns", server.Iam.Created)
	}
	held := holdings(server.Iam, p.Clients, ownedMember)
	if len(held) == 0 {
		t.Errorf("the patched account holds nothing, want ocel's grants on its own records and key to reach it")
	}
	if own := holdings(server.Iam, p.Clients, appMember(p.Clients, spec.Ref, "api")); len(own) != 0 {
		t.Errorf("ocel's own account for the app holds %q, want nothing", own)
	}
}

func TestAPatchedAccountIsGrantedOcelsResourcesAndNothingOnItself(t *testing.T) {
	server := appAccountsOnly()
	server.AccountPolicies = ownAccountHolding(&iam.Binding{Role: bootstrap.RunAsRole, Members: []string{ownedMember, tasksAgent}})
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	email, err := ensureRunningAs(t, p, c, spec, runningAs(ownedAccount), map[string]*provider.TopicSpec{"resize": {}})
	if err != nil {
		t.Fatalf("EnsureAccount() = %v", err)
	}

	if email != ownedAccount {
		t.Errorf("the app runs as %q, want %s", email, ownedAccount)
	}
	if server.AccountWrites != 0 {
		t.Errorf("wrote %d account policies, want none: ocel never grants on an account it does not own", server.AccountWrites)
	}
	if !slices.ContainsFunc(projectBindingsOf(server), func(bound string) bool { return strings.Contains(bound, ownedMember) }) {
		t.Errorf("the project binds %q, want the patched account to record runs in its project's database", projectBindingsOf(server))
	}
}

func TestAnAppPatchedToRunAsAnAccountSignsItsDelayedMessagesAsThatAccount(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	server.Iam.AccountPolicies = ownAccountHolding(&iam.Binding{Role: bootstrap.RunAsRole, Members: []string{ownedMember, tasksAgent}})
	p := servingTransformed(t, server, runningAs(ownedAccount))

	if err := provisionTransformedFunctions(t, p, reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	pinned := googletest.EnvOf(server.Created[0].Template.Containers[0])[manifest.EnvVar]
	if !strings.Contains(pinned, ownedAccount) || strings.Contains(pinned, p.Clients.AppAccountEmail("shop", "production", "api")) {
		t.Errorf("the app is told to sign its delayed messages per %s, want as %s, the account it runs as", pinned, ownedAccount)
	}
}

func TestAPatchedAccountThatDoesNotGrantWhatTheAppNeedsOnItIsRefusedNamingIt(t *testing.T) {
	server := appAccountsOnly()
	server.AccountPolicies = ownAccountHolding(&iam.Binding{Role: bootstrap.RunAsRole, Members: []string{ownedMember}})
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	_, err := ensureRunningAs(t, p, c, spec, runningAs(ownedAccount), map[string]*provider.TopicSpec{"resize": {}})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("EnsureAccount() = %v, want a denied refusal", err)
	}
	if want := bootstrap.RunAsRole + " to " + tasksAgent; !strings.Contains(refused.Message, want) || strings.Contains(refused.Message, " to "+ownedMember) {
		t.Errorf("refused with %q, want it to name exactly what is missing, %q", refused.Message, want)
	}
	if server.AccountWrites != 0 || server.ProjectWrites != 0 {
		t.Errorf("wrote %d account and %d project policies, want nothing granted before the account is found to hold what the app needs", server.AccountWrites, server.ProjectWrites)
	}
}

func TestAPatchedAccountWhoseProjectGrantsWhatTheAppNeedsOnItIsTrusted(t *testing.T) {
	for name, role := range map[string]string{
		"the role ocel grants": bootstrap.RunAsRole,
		"a broader role":       "roles/iam.serviceAccountAdmin",
		"a custom role":        "projects/acme-prod/roles/runner",
	} {
		t.Run(name, func(t *testing.T) {
			server := appAccountsOnly()
			server.AccountPolicies = ownAccountHolding(&iam.Binding{Role: bootstrap.RunAsRole, Members: []string{ownedMember}})
			server.Project.Bindings = append(server.Project.Bindings, &cloudresourcemanager.Binding{Role: role, Members: []string{tasksAgent}})
			server.Roles = map[string][]string{
				"roles/iam.serviceAccountAdmin":   {"iam.serviceAccounts.actAs", "iam.serviceAccounts.get"},
				"projects/acme-prod/roles/runner": {"iam.serviceAccounts.actAs"},
			}
			p, c := ensuringAccounts(t, server)

			_, err := ensureRunningAs(t, p, c, reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})), runningAs(ownedAccount), map[string]*provider.TopicSpec{"resize": {}})
			if err != nil {
				t.Errorf("EnsureAccount() = %v, want the account trusted: the project lets the Cloud Tasks agent act as every account in it", err)
			}
		})
	}
}

func TestAPatchedAccountWhoseProjectGrantsARoleWithoutThePermissionIsRefused(t *testing.T) {
	server := appAccountsOnly()
	server.AccountPolicies = ownAccountHolding(&iam.Binding{Role: bootstrap.RunAsRole, Members: []string{ownedMember}})
	server.Project.Bindings = append(server.Project.Bindings, &cloudresourcemanager.Binding{Role: "roles/iam.serviceAccountViewer", Members: []string{tasksAgent}})
	server.Roles = map[string][]string{"roles/iam.serviceAccountViewer": {"iam.serviceAccounts.get"}}
	p, c := ensuringAccounts(t, server)

	_, err := ensureRunningAs(t, p, c, reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})), runningAs(ownedAccount), map[string]*provider.TopicSpec{"resize": {}})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, bootstrap.RunAsRole+" to "+tasksAgent) {
		t.Errorf("EnsureAccount() = %v, want a denied refusal naming %s to %s: the project's role holds no permission to act as the account", err, bootstrap.RunAsRole, tasksAgent)
	}
}

func TestAPatchedAccountOfAnotherProjectIsTrustedOnlyForWhatItsOwnProjectGrants(t *testing.T) {
	const (
		foreign       = "runner@shared-ids.iam.gserviceaccount.com"
		foreignMember = "serviceAccount:" + foreign
	)
	actAs := &cloudresourcemanager.Binding{Role: bootstrap.RunAsRole, Members: []string{tasksAgent}}
	for name, tc := range map[string]struct {
		home, here []*cloudresourcemanager.Binding
		trusted    bool
	}{
		"its own project grants it": {home: []*cloudresourcemanager.Binding{actAs}, trusted: true},
		"only ocel's project does":  {here: []*cloudresourcemanager.Binding{actAs}},
		"neither project grants it": {},
	} {
		t.Run(name, func(t *testing.T) {
			server := appAccountsOnly()
			server.AccountPolicies = map[string]*iam.Policy{"/v1/projects/-/serviceAccounts/" + foreign: {Etag: "BwXhoLA=", Bindings: []*iam.Binding{{Role: bootstrap.RunAsRole, Members: []string{foreignMember}}}}}
			server.ProjectPolicies = map[string]*cloudresourcemanager.Policy{"shared-ids": {Etag: "BwXhoLA=", Bindings: tc.home}}
			server.Project.Bindings = append(server.Project.Bindings, tc.here...)
			p, c := ensuringAccounts(t, server)

			_, err := ensureRunningAs(t, p, c, reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})), runningAs(foreign), map[string]*provider.TopicSpec{"resize": {}})

			if tc.trusted {
				if err != nil {
					t.Errorf("EnsureAccount() = %v, want the account trusted: project shared-ids lets the Cloud Tasks agent act as every account in it", err)
				}
				return
			}
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, "project shared-ids") || strings.Contains(refused.Message, "acme-prod") {
				t.Errorf("EnsureAccount() = %v, want a denied refusal naming project shared-ids, the account's own, and not acme-prod, whose grants reach only its own accounts", err)
			}
		})
	}
}

func TestAPatchedAccountOutsideAnyProjectsDomainIsCheckedOnItsOwnPolicyAlone(t *testing.T) {
	for name, account := range map[string]string{
		"a default account": "123456789-compute@developer.gserviceaccount.com",
		"a service agent":   "service-123456789@gcp-sa-runner.iam.gserviceaccount.com",
	} {
		t.Run(name, func(t *testing.T) {
			server := appAccountsOnly()
			server.AccountPolicies = map[string]*iam.Policy{"/v1/projects/-/serviceAccounts/" + account: {Etag: "BwXhoLA=", Bindings: []*iam.Binding{{Role: bootstrap.RunAsRole, Members: []string{"serviceAccount:" + account}}}}}
			server.Project.Bindings = append(server.Project.Bindings, &cloudresourcemanager.Binding{Role: bootstrap.RunAsRole, Members: []string{tasksAgent}})
			p, c := ensuringAccounts(t, server)

			_, err := ensureRunningAs(t, p, c, reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})), runningAs(account), map[string]*provider.TopicSpec{"resize": {}})

			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, bootstrap.RunAsRole+" to "+tasksAgent) || strings.Contains(refused.Message, "project") {
				t.Errorf("EnsureAccount() = %v, want a denied refusal naming %s to %s on the account and no project: its domain names none whose grants reach it", err, bootstrap.RunAsRole, tasksAgent)
			}
		})
	}
}

func TestAnAccountTheDeployCredentialMayNotActAsIsRefusedUnlessChecksAreSkipped(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		server := appAccountsOnly()
		server.AccountPermissions = map[string][]string{ownedAccount: nil}
		p, c := ensuringAccounts(t, server)
		p.SkipChecks = skipped

		_, err := ensureRunningAs(t, p, c, functionStackDeclaring(environment.TierProduction, "production", openingASecret), runningAs(ownedAccount), nil)

		if skipped {
			if err != nil {
				t.Errorf("EnsureAccount() with checks skipped = %v, want the account trusted", err)
			}
			continue
		}
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, bootstrap.RunAsRole) || !strings.Contains(refused.Message, ownedAccount) {
			t.Errorf("EnsureAccount() = %v, want a denied refusal naming %s on %s", err, bootstrap.RunAsRole, ownedAccount)
		}
	}
}

func TestAPlanRunningAnAppAsAnAccountTheDeployCredentialMayNotActAsIsRefused(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	server.Iam.AccountPermissions = map[string][]string{ownedAccount: nil}
	p := servingTransformed(t, server, runningAs(ownedAccount))

	_, err := p.Stacks().Plan(context.Background(), functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), &fake.Log{})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, ownedAccount) {
		t.Errorf("Plan() = %v, want a denied refusal naming %s", err, ownedAccount)
	}
}

func TestAPlanRunningAnAppAsAnAccountThatDoesNotGrantWhatTheAppNeedsOnItIsRefused(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	server.Iam.AccountPolicies = ownAccountHolding(&iam.Binding{Role: bootstrap.RunAsRole, Members: []string{ownedMember}})
	p := servingTransformed(t, server, runningAs(ownedAccount))

	_, err := p.Stacks().Plan(context.Background(), reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})), &fake.Log{})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, bootstrap.RunAsRole+" to "+tasksAgent) {
		t.Errorf("Plan() = %v, want the denied refusal the deploy would answer, naming %s to %s", err, bootstrap.RunAsRole, tasksAgent)
	}
}

func TestServicesOfOneAppPatchedToDifferentAccountsAreRefused(t *testing.T) {
	server := &googletest.Run{}
	p := servingTransformed(t, server, patchingEach{
		"fn--api--index": {"service": {"template": map[string]any{"serviceAccount": ownedAccount}}},
	})
	spec := withWorker(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), "media")

	err := provisionTransformedFunctions(t, p, spec)

	refusedNaming(t, err, "function fn--api--index as "+ownedAccount, "worker media as the app's own account")
	if len(server.Created) != 0 {
		t.Errorf("created %d services, want none", len(server.Created))
	}
}

func TestTwoAppsPatchedToRunAsOneAccountAreRefused(t *testing.T) {
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	spec := functionStackDeclaring(environment.TierProduction, "production", openingASecret)
	if _, err := ensureRunningAs(t, p, c, spec, runningAs(ownedAccount), nil); err != nil {
		t.Fatalf("EnsureAccount(api) = %v", err)
	}

	_, err := ensureRunningAs(t, p, c, appNamed("web", spec), runningAs(ownedAccount), nil)

	refusedNaming(t, err, "app web of shop", "app api of shop", ownedAccount)
}

func TestADeployRefusedForWhatItsPatchedAccountLacksLeavesTheAccountFreeForAnotherApp(t *testing.T) {
	server := appAccountsOnly()
	server.AccountPolicies = ownAccountHolding(&iam.Binding{Role: bootstrap.RunAsRole, Members: []string{ownedMember}})
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	if _, err := ensureRunningAs(t, p, c, spec, runningAs(ownedAccount), map[string]*provider.TopicSpec{"resize": {}}); err == nil {
		t.Fatal("EnsureAccount(api) = nil, want a refusal: the account does not let the Cloud Tasks agent act as it")
	}

	_, err := ensureRunningAs(t, p, c, appNamed("web", functionStackDeclaring(environment.TierProduction, "production", openingASecret)), runningAs(ownedAccount), nil)
	if err != nil {
		t.Errorf("EnsureAccount(web) = %v, want the account free: the refused deploy of api never ran as it", err)
	}
}

func TestADeployWhoseGrantsFailLeavesTheAccountFreeOnceItsStackIsRemoved(t *testing.T) {
	server := appAccountsOnly()
	server.ProjectDenied = true
	p, c := ensuringAccounts(t, server)
	spec := functionStackDeclaring(environment.TierProduction, "production", openingASecret)
	if _, err := ensureRunningAs(t, p, c, spec, runningAs(ownedAccount), nil); err == nil {
		t.Fatal("EnsureAccount(api) = nil, want the project's refusal of its grants")
	}
	server.ProjectDenied = false

	removeStack(t, p, c, spec.Ref)

	if _, err := ensureRunningAs(t, p, c, appNamed("web", spec), runningAs(ownedAccount), nil); err != nil {
		t.Errorf("EnsureAccount(web) = %v, want the account free once the failed release of api is removed", err)
	}
}

func TestEnvironmentsOfAnAppShareAPatchedAccountAndRemovingOneKeepsWhatTheOthersHold(t *testing.T) {
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	first := functionStackDeclaring(environment.TierPreview, "pr-1", openingASecret)
	second := functionStackDeclaring(environment.TierPreview, "pr-2", openingASecret)
	for _, spec := range []provider.StackSpec{first, second} {
		if _, err := ensureRunningAs(t, p, c, spec, runningAs(ownedAccount), nil); err != nil {
			t.Fatalf("EnsureAccount(%s) = %v", spec.Ref.Name.Env, err)
		}
	}
	shared := "roles/cloudkms.cryptoKeyDecrypter " + ownedMember + " on projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/preview"
	if !slices.Contains(keyBindingsOf(server), shared) {
		t.Fatalf("the keys bind %q, want %q", keyBindingsOf(server), shared)
	}

	removeStack(t, p, c, first.Ref)

	if !slices.Contains(keyBindingsOf(server), shared) {
		t.Errorf("the keys bind %q after pr-1 was removed, want %q kept: pr-2 runs as the same account and opens under the same key", keyBindingsOf(server), shared)
	}
	removeStack(t, p, c, second.Ref)
	if got := holdings(server, c, ownedMember); len(got) != 0 {
		t.Errorf("the account holds %q once no environment runs as it, want nothing", got)
	}
}

func provisionReleasesSwitching(t *testing.T, p *Provider, first, then transform.Pass) (provider.StackSpec, provider.StackSpec) {
	t.Helper()
	before := functionStackDeclaring(environment.TierProduction, "production", openingASecret)
	before.Ref.Name = stackOf("production", "api", "r1")
	after := before
	after.Ref.Name = stackOf("production", "api", "r2")
	for _, each := range []struct {
		spec provider.StackSpec
		pass transform.Pass
	}{{before, first}, {after, then}} {
		p.transforms = each.pass
		deployed, err := p.ProvisionFunctions(evaluatedFor(t, p, each.spec), each.spec, &fake.Log{})
		if err != nil {
			t.Fatalf("ProvisionFunctions(%s) = %v", each.spec.Ref.Name, err)
		}
		if each.spec.Ref.Name == after.Ref.Name {
			recordStackFunctions(t, p, each.spec, deployed)
		}
	}
	return before, after
}

func TestAReleaseSwitchedToAPatchedAccountLeavesTheReleaseBeforeItItsGrantsUntilThatOneIsRemoved(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	p := servingTransformed(t, server, &patching{})
	before, _ := provisionReleasesSwitching(t, p, &patching{}, runningAs(ownedAccount))
	own := appMember(p.Clients, before.Ref, "api")

	if len(holdings(server.Iam, p.Clients, own)) == 0 {
		t.Fatalf("ocel's account holds nothing once r2 runs as %s, want its grants kept: r1 still runs as it", ownedAccount)
	}
	removeStack(t, p, p.Clients, before.Ref)

	if got := holdings(server.Iam, p.Clients, own); len(got) != 0 {
		t.Errorf("ocel's account holds %q once r1 is removed, want nothing: no release runs as it", got)
	}
}

func TestAReleaseSwitchedBackToOcelsAccountLeavesThePatchedOneItsGrantsUntilTheReleaseBeforeItIsRemoved(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	p := servingTransformed(t, server, &patching{})
	before, after := provisionReleasesSwitching(t, p, runningAs(ownedAccount), &patching{})

	if len(holdings(server.Iam, p.Clients, ownedMember)) == 0 {
		t.Fatalf("the patched account holds nothing once r2 runs as ocel's, want its grants kept: r1 still runs as it")
	}
	removeStack(t, p, p.Clients, before.Ref)

	if got := holdings(server.Iam, p.Clients, ownedMember); len(got) != 0 {
		t.Errorf("the patched account holds %q once r1 is removed, want nothing: no release runs as it", got)
	}
	if _, err := ensureRunningAs(t, p, p.Clients, appNamed("web", after), runningAs(ownedAccount), nil); err != nil {
		t.Errorf("EnsureAccount(web as the freed account) = %v, want the account free for another app", err)
	}
}

func TestRemovingTheLastReleaseThatRanAsOcelsAccountDeletesIt(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	p := servingTransformed(t, server, &patching{})
	before := functionStackDeclaring(environment.TierProduction, "production", openingASecret)
	before.Ref.Name = stackOf("production", "api", "r1")
	after := before
	after.Ref.Name = stackOf("production", "api", "r2")
	if err := provisionTransformedFunctions(t, p, before); err != nil {
		t.Fatalf("ProvisionFunctions(r1) = %v", err)
	}
	p.transforms = runningAs(ownedAccount)
	ctx := evaluatedFor(t, p, after)
	deployed, err := p.ProvisionFunctions(ctx, after, &fake.Log{})
	if err != nil {
		t.Fatalf("ProvisionFunctions(r2) = %v", err)
	}
	recordStackFunctions(t, p, after, deployed)
	ownPath := accountPathOf(p.Clients, before.Ref, "api")

	removeStack(t, p, p.Clients, before.Ref)

	if !slices.Contains(server.Iam.DeletedAccounts, ownPath) {
		t.Errorf("deleted the accounts %q, want %s: the release left runs as the patched account, and nothing runs as ocel's any more", server.Iam.DeletedAccounts, ownPath)
	}
	if len(holdings(server.Iam, p.Clients, ownedMember)) == 0 {
		t.Errorf("the patched account holds nothing after the old release was removed, want its grants kept for the release that runs as it")
	}
}

func TestRemovingTheNewestReleaseFreesThePatchedAccountItAloneRanAs(t *testing.T) {
	server := &googletest.Run{Iam: appAccountsOnly()}
	p := servingTransformed(t, server, &patching{})
	before := functionStackDeclaring(environment.TierProduction, "production", openingASecret)
	before.Ref.Name = stackOf("production", "api", "r1")
	after := before
	after.Ref.Name = stackOf("production", "api", "r2")
	var going []string
	for _, each := range []struct {
		spec provider.StackSpec
		pass transform.Pass
	}{{before, &patching{}}, {after, runningAs(ownedAccount)}} {
		p.transforms = each.pass
		deployed, err := p.ProvisionFunctions(evaluatedFor(t, p, each.spec), each.spec, &fake.Log{})
		if err != nil {
			t.Fatalf("ProvisionFunctions(%s) = %v", each.spec.Ref.Name, err)
		}
		recordStackFunctions(t, p, each.spec, deployed)
		going = nil
		for _, function := range deployed {
			going = append(going, function.Name)
		}
	}

	if err := p.release().DeleteUnusedAccount(context.Background(), p.Clients, after.Ref, going, nil, &fake.Log{}); err != nil {
		t.Fatalf("DeleteUnusedAccount(r2) = %v", err)
	}

	if got := holdings(server.Iam, p.Clients, ownedMember); len(got) != 0 {
		t.Errorf("the patched account holds %q once r2 is reclaimed, want nothing: r1 runs as ocel's account", got)
	}
	if len(holdings(server.Iam, p.Clients, appMember(p.Clients, before.Ref, "api"))) == 0 {
		t.Errorf("ocel's account holds nothing once r2 is reclaimed, want its grants kept: r1 runs as it")
	}
	if _, err := ensureRunningAs(t, p, p.Clients, appNamed("web", after), runningAs(ownedAccount), nil); err != nil {
		t.Errorf("EnsureAccount(web as the freed account) = %v, want the account free for another app", err)
	}
}
