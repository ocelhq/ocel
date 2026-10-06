package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type recordedStacks struct {
	keyvalue.Store
	names []naming.StackName
}

func (r recordedStacks) List(_ context.Context, in keyvalue.Partition, _ ...string) ([]keyvalue.Entry, error) {
	entries := make([]keyvalue.Entry, 0, len(r.names))
	for _, name := range r.names {
		entries = append(entries, keyvalue.Entry{Key: keyvalue.Key{Partition: in, Path: []string{name.String()}}})
	}
	return entries, nil
}

func releaseOf(id string) naming.Release { return naming.NewRelease(id, "fingerprint") }

func stackOf(env, app, release string) naming.StackName {
	return naming.AppStack(env, app, releaseOf(release))
}

func grantedAppAccount(t *testing.T, server *iamServer) (*clients, provider.StackSpec, string) {
	t.Helper()
	server.accountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(routedNextSpec())
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	email, err := p.ensureAppAccount(context.Background(), c, spec, map[string]*provider.TopicSpec{"resize": {}})
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}
	if err := grantCache(context.Background(), c, spec, email); err != nil {
		t.Fatalf("grantCache() = %v", err)
	}
	return c, spec, "serviceAccount:" + email
}

func holdings(server *iamServer, c *clients, member string) []string {
	var held []string
	for _, bound := range projectBindingsOf(server) {
		if strings.Contains(bound, member) {
			held = append(held, bound)
		}
	}
	queue := c.DelayQueuePath("europe-west1", environment.TierProduction)
	for _, binding := range server.queuePolicies[queue].GetBindings() {
		if slices.Contains(binding.GetMembers(), member) {
			held = append(held, "queue "+binding.GetRole())
		}
	}
	delay := "/v1/projects/acme-prod/serviceAccounts/" + c.DelayAccountEmail(environment.TierProduction)
	for _, binding := range policyOrEmpty(server.accountPolicies[delay]) {
		if slices.Contains(binding.Members, member) {
			held = append(held, "delay account "+binding.Role)
		}
	}
	return held
}

func TestRemovingTheLastEnvironmentRunningAnAppRevokesEveryGrantItsAccountHeld(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	if got := holdings(server, c, member); len(got) != 8 {
		t.Fatalf("the account holds %q before removal, want the records, key, task database, tag database, cache, queue (2) and delay account grants", got)
	}
	records := recordedStacks{names: []naming.StackName{spec.Ref.Name, naming.InfraStack("production"), stackOf("production", "api", "r1")}}

	if err := forgetUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("forgetUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); len(got) != 0 {
		t.Errorf("the account still holds %q after the last environment running its app was removed, want nothing", got)
	}
}

func TestRemovingOneEnvironmentLeavesTheGrantsOfAnAppAnotherEnvironmentRuns(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	before := holdings(server, c, member)
	records := recordedStacks{names: []naming.StackName{spec.Ref.Name, stackOf("pr-8", "web", "r2")}}

	if err := forgetUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("forgetUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q, want the %q it held while pr-8 still runs the app", got, before)
	}
}

func TestDestroyingOneReleaseLeavesTheGrantsOfAnAppWhoseOtherReleaseIsRecorded(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	before := holdings(server, c, member)
	older := stackOf(spec.Ref.Name.Env, "web", "r0")
	records := recordedStacks{names: []naming.StackName{spec.Ref.Name, older}}
	ref := spec.Ref
	ref.Name = older

	if err := forgetUnusedAppAccount(context.Background(), c, records, ref, nil); err != nil {
		t.Fatalf("forgetUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q, want the %q it held while another release of the app is recorded", got, before)
	}
}

func TestRevokingAnAppsGrantsTouchesNoOtherMembersBindings(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	other := "serviceAccount:other@acme-prod.iam.gserviceaccount.com"
	for _, binding := range server.project.Bindings {
		if binding.Role == appRecordsRole {
			binding.Members = append(binding.Members, other)
		}
	}
	server.project.Bindings = append(server.project.Bindings, &cloudresourcemanager.Binding{Role: "roles/viewer", Members: []string{other}})

	if err := forgetUnusedAppAccount(context.Background(), c, recordedStacks{names: []naming.StackName{spec.Ref.Name}}, spec.Ref, nil); err != nil {
		t.Fatalf("forgetUnusedAppAccount() = %v", err)
	}

	want := []string{
		appRecordsRole + " " + other + ` resource.name == "projects/acme-prod/databases/ocel"`,
		"roles/viewer " + other + " ",
	}
	if got := projectBindingsOf(server); !slices.Equal(got, want) {
		t.Errorf("the project binds %q, want exactly %q: the other member stays and every binding left empty is dropped", got, want)
	}
	for _, binding := range server.project.Bindings {
		if len(binding.Members) == 0 {
			t.Errorf("the project keeps the empty binding %+v", binding)
		}
	}
	if server.project.Version != conditionalPolicyVersion {
		t.Errorf("the policy was written at version %d, want %d: its bindings are conditional", server.project.Version, conditionalPolicyVersion)
	}
	if member == other {
		t.Fatal("the app's member is the other member")
	}
}

func TestRevokingAnAppsGrantsOnATierWithoutTasksSucceeds(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	server.tasksAbsent = true

	if err := forgetUnusedAppAccount(context.Background(), c, recordedStacks{names: []naming.StackName{spec.Ref.Name}}, spec.Ref, nil); err != nil {
		t.Fatalf("forgetUnusedAppAccount() = %v, want a tier with no delay queue or account to have nothing to revoke there", err)
	}

	if got := projectBindingsOf(server); slices.ContainsFunc(got, func(bound string) bool { return strings.Contains(bound, member) }) {
		t.Errorf("the project still binds %q, want the member gone from it", got)
	}
}

func TestRemovingAnInfraStackRevokesNoAppAccount(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, _, member := grantedAppAccount(t, server)
	before := holdings(server, c, member)

	if err := forgetUnusedAppAccount(context.Background(), c, recordedStacks{}, provider.StackRef{
		Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack(stackrecords.ProductionEnv),
	}, nil); err != nil {
		t.Fatalf("forgetUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q, want the %q it held: an infra stack runs no app", got, before)
	}
}

func TestDestroyingTheLastReleaseOfAnAppRevokesItsAccountAndAnotherReleaseKeepsIt(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	first := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one")
	second := functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:two")
	released.provision(t, first)
	released.provision(t, second)
	member := "serviceAccount:" + p.resolved.AppAccountEmail(environment.TierProduction, "shop", "web")
	holding := func() bool {
		return slices.ContainsFunc(projectBindingsOf(server.identities()), func(bound string) bool { return strings.Contains(bound, member) })
	}
	if !holding() {
		t.Fatalf("the project binds %q before any destroy, want the app's account in it", projectBindingsOf(server.identities()))
	}

	released.destroy(t, first.Ref)
	if !holding() {
		t.Error("destroying one release revoked the account while another release of the app is recorded")
	}
	released.destroy(t, second.Ref)
	if holding() {
		t.Errorf("the project still binds %q after the app's last release was destroyed", projectBindingsOf(server.identities()))
	}
}

func containerEnvironment(env string) provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierPreview,
			Name:    naming.AppStack(env, "web", releaseOf("r1")),
		},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:             "web",
			Compute:         provider.ComputeContainer,
			Image:           "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
			HealthCheckPath: "/",
		},
	}
}

func (r releasedStacks) provisionContainer(t *testing.T, spec provider.StackSpec) {
	t.Helper()
	ctx := context.Background()
	result, err := r.stacks.Provision(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Provision(%s) = %v", spec.Ref.Name, err)
	}
	if len(result.Containers) != 1 {
		t.Fatalf("Provision(%s) deployed %+v, want the one container its spec names", spec.Ref.Name, result.Containers)
	}
	if err := stackrecords.Write(ctx, r.store, spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name,
		stackrecords.Stack{Kind: provider.StackApp, App: spec.App.App, Containers: result.Containers}); err != nil {
		t.Fatal(err)
	}
}

func TestDestroyingTheContainerAppOfTheLastEnvironmentRunningItRevokesItsAccountAndAnotherEnvironmentKeepsIt(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	first, second := containerEnvironment("pr-7"), containerEnvironment("pr-8")
	released.provisionContainer(t, first)
	released.provisionContainer(t, second)
	member := "serviceAccount:" + p.resolved.AppAccountEmail(environment.TierPreview, "shop", "web")
	holding := func() bool {
		return slices.ContainsFunc(projectBindingsOf(server.identities()), func(bound string) bool { return strings.Contains(bound, member) })
	}
	if !holding() {
		t.Fatalf("the project binds %q before any destroy, want the app's account in it", projectBindingsOf(server.identities()))
	}

	released.destroy(t, first.Ref)
	if !holding() {
		t.Error("destroying the container app of pr-7 revoked the account while pr-8 still runs the app")
	}
	released.destroy(t, second.Ref)
	if holding() {
		t.Errorf("the project still binds %q after the last environment running the container app was destroyed", projectBindingsOf(server.identities()))
	}
}
