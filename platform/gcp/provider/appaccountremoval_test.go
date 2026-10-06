package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
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
	return grantedAccountOf(t, server, reachingTopics(routedNextSpec()), map[string]*provider.TopicSpec{"resize": {}})
}

func grantedRefreshOnlyAppAccount(t *testing.T, server *iamServer) (*clients, provider.StackSpec, string) {
	t.Helper()
	return grantedAccountOf(t, server, routedNextSpec(), nil)
}

func grantedAccountOf(t *testing.T, server *iamServer, spec provider.StackSpec, declared map[string]*provider.TopicSpec) (*clients, provider.StackSpec, string) {
	t.Helper()
	server.accountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	email, err := p.ensureAppAccount(context.Background(), c, spec, declared)
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
	own := "/v1/projects/acme-prod/serviceAccounts/" + strings.TrimPrefix(member, "serviceAccount:")
	for _, binding := range policyOrEmpty(server.accountPolicies[own]) {
		if slices.Contains(binding.Members, member) {
			held = append(held, "own account "+binding.Role)
		}
	}
	refresh := "/v1/projects/acme-prod/serviceAccounts/" + c.RefreshAccountEmail(environment.TierProduction)
	for _, binding := range policyOrEmpty(server.accountPolicies[refresh]) {
		if slices.Contains(binding.Members, member) {
			held = append(held, "refresh account "+binding.Role)
		}
	}
	return held
}

func TestRemovingTheLastEnvironmentRunningAnAppRevokesEveryGrantItsAccountHeld(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	if got := holdings(server, c, member); len(got) != 8 {
		t.Fatalf("the account holds %q before removal, want the records, key, task database, tag database, cache, queue, own account and refresh account grants", got)
	}
	records := recordedStacks{names: []naming.StackName{spec.Ref.Name, naming.InfraStack("production"), stackOf("production", "api", "r1")}}

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
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

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
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

	if err := revokeUnusedAppAccount(context.Background(), c, records, ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
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

	if err := revokeUnusedAppAccount(context.Background(), c, recordedStacks{names: []naming.StackName{spec.Ref.Name}}, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
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

	if err := revokeUnusedAppAccount(context.Background(), c, recordedStacks{names: []naming.StackName{spec.Ref.Name}}, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v, want a tier with no delay queue or account to have nothing to revoke there", err)
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

	if err := revokeUnusedAppAccount(context.Background(), c, recordedStacks{}, provider.StackRef{
		Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack(stackrecords.ProductionEnv),
	}, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
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

func topicBinding(declared string) provider.Binding {
	return provider.Binding{
		Type: provider.BindingTopic, Name: "topic--" + declared, Resource: declared,
		Properties: map[string]string{topicPathProperty: declared, topicDeclaredProperty: declared, topicSpecProperty: "{}"},
	}
}

func recordedTierTopics(t *testing.T, spec provider.StackSpec, other ...naming.StackName) keyvalue.Store {
	t.Helper()
	store := fake.NewKeyValues()
	write := func(name naming.StackName, recorded stackrecords.Stack) {
		if err := stackrecords.Write(context.Background(), store, spec.Ref.Tier, spec.Ref.Project, name, recorded); err != nil {
			t.Fatal(err)
		}
	}
	write(spec.Ref.Name, stackrecords.Stack{Kind: provider.StackApp, App: "web"})
	for _, env := range []string{stackrecords.ProductionEnv, "pr-6"} {
		write(naming.InfraStack(env), stackrecords.Stack{Kind: provider.StackInfra, Bindings: []provider.Binding{topicBinding("resize")}})
	}
	for _, name := range other {
		write(name, stackrecords.Stack{Kind: provider.StackApp, App: name.App})
	}
	return store
}

func resizePath(c *clients, spec provider.StackSpec, env string) string {
	ref := spec.Ref
	ref.Name = naming.InfraStack(env)
	return "/v1/projects/acme-prod/topics/" + taskNames(c.Names, ref).Topic("resize")
}

func publishersOf(server *iamServer, path string) []string {
	server.mu.Lock()
	defer server.mu.Unlock()
	for _, binding := range topicBindingsOf(server.topicPolicies[path]) {
		if binding.Role == "roles/pubsub.publisher" {
			return binding.Members
		}
	}
	return nil
}

func TestRemovingTheLastEnvironmentRunningAnAppTakesItsAccountOffEveryTopicOfTheTier(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	other := "serviceAccount:other@acme-prod.iam.gserviceaccount.com"
	prod, preview := resizePath(c, spec, stackrecords.ProductionEnv), resizePath(c, spec, "pr-6")
	if !slices.Contains(publishersOf(server, prod), member) {
		t.Fatalf("prod's resize has publishers %q before removal, want the account in them", publishersOf(server, prod))
	}
	server.topicPolicies[preview] = &pubsub.Policy{Etag: "BwXhoLA=", Bindings: []*pubsub.Binding{{Role: "roles/pubsub.publisher", Members: []string{member, other}}}}

	if err := revokeUnusedAppAccount(context.Background(), c, recordedTierTopics(t, spec), spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if got := publishersOf(server, prod); slices.Contains(got, member) {
		t.Errorf("prod's resize still has publishers %q, want the account gone", got)
	}
	if got := publishersOf(server, preview); !slices.Equal(got, []string{other}) {
		t.Errorf("pr-6's resize has publishers %q, want only the other member", got)
	}
}

func TestRemovingOneEnvironmentLeavesTheAppsTopicGrants(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	writes := server.topicWrites

	if err := revokeUnusedAppAccount(context.Background(), c, recordedTierTopics(t, spec, stackOf("pr-8", "web", "r2")), spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if server.topicWrites != writes {
		t.Errorf("topic policies were written %d times, want none while pr-8 runs the app", server.topicWrites-writes)
	}
	if got := publishersOf(server, resizePath(c, spec, stackrecords.ProductionEnv)); !slices.Contains(got, member) {
		t.Errorf("prod's resize has publishers %q, want the account kept", got)
	}
}

func TestRevokingAnAppsTopicGrantsSkipsATopicThatIsGone(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	server.deletedTopics = map[string]bool{resizePath(c, spec, stackrecords.ProductionEnv): true}

	if err := revokeUnusedAppAccount(context.Background(), c, recordedTierTopics(t, spec), spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v, want a deleted topic to have nothing to revoke", err)
	}

	if got := projectBindingsOf(server); slices.ContainsFunc(got, func(bound string) bool { return strings.Contains(bound, member) }) {
		t.Errorf("the project still binds %q, want the member gone from it", got)
	}
}

type racingStacks struct {
	keyvalue.Store
	between func()
	lists   *int
}

func (r racingStacks) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	entries, err := r.Store.List(ctx, in, under...)
	*r.lists++
	if *r.lists == 1 && r.between != nil {
		r.between()
	}
	return entries, err
}

func topicHeld(server *iamServer, c *clients, ref provider.StackRef, member string) bool {
	return slices.Contains(publishersOf(server, "/v1/projects/acme-prod/topics/"+taskNames(c.Names, ref).Topic("resize")), member)
}

func racingRecords(t *testing.T, spec provider.StackSpec, between func(), lists *int) (keyvalue.Store, keyvalue.Store) {
	t.Helper()
	store := fake.NewKeyValues()
	ctx := context.Background()
	if err := stackrecords.Write(ctx, store, spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name, stackrecords.Stack{Kind: provider.StackApp, App: "web"}); err != nil {
		t.Fatal(err)
	}
	infra := naming.InfraStack(stackrecords.ProductionEnv)
	if err := stackrecords.Write(ctx, store, spec.Ref.Tier, spec.Ref.Project, infra,
		stackrecords.Stack{Kind: provider.StackInfra, Bindings: []provider.Binding{topicBinding("resize")}}); err != nil {
		t.Fatal(err)
	}
	return racingStacks{Store: store, between: between, lists: lists}, store
}

func recordApp(t *testing.T, store keyvalue.Store, spec provider.StackSpec, name naming.StackName) {
	t.Helper()
	if err := stackrecords.Write(context.Background(), store, spec.Ref.Tier, spec.Ref.Project, name, stackrecords.Stack{Kind: provider.StackApp, App: "web"}); err != nil {
		t.Error(err)
	}
}

func TestAnEnvironmentRecordedWhileAnAppsGrantsAreRevokedKeepsThem(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	before := holdings(server, c, member)
	if !topicHeld(server, c, spec.Ref, member) {
		t.Fatal("the account holds no topic publisher before removal")
	}
	writes, lists := server.projectWrites, 0
	var backing keyvalue.Store
	records, backing := racingRecords(t, spec, func() {
		recordApp(t, backing, spec, stackOf("pr-8", "web", "r2"))
	}, &lists)

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q, want the %q it held before pr-8 started running the app", got, before)
	}
	if !topicHeld(server, c, spec.Ref, member) {
		t.Error("the account lost its topic publisher grant")
	}
	if lists != 2 {
		t.Errorf("the records were listed %d times, want 2", lists)
	}
	if server.projectWrites-writes < 2 {
		t.Errorf("the project policy was written %d times, want a revoke and a restore", server.projectWrites-writes)
	}
}

func TestGrantsADeployMadeBeforeTheRevocationLandedAreRestored(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.accountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(routedNextSpec())
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	member := "serviceAccount:" + c.AppAccountEmail(spec.Ref.Tier, spec.Ref.Project, "web")
	lists := 0
	var backing keyvalue.Store
	records, backing := racingRecords(t, spec, func() {
		recordApp(t, backing, spec, stackOf("pr-8", "web", "r2"))
		email, err := p.ensureAppAccount(context.Background(), c, spec, map[string]*provider.TopicSpec{"resize": {}})
		if err != nil {
			t.Error(err)
			return
		}
		if err := grantCache(context.Background(), c, spec, email); err != nil {
			t.Error(err)
		}
	}, &lists)

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); len(got) != 8 {
		t.Errorf("the account holds %q, want the 8 grants the deploy made while the revoke ran", got)
	}
	if !topicHeld(server, c, spec.Ref, member) {
		t.Error("the account lost its topic publisher grant")
	}
}

func TestGrantsStayRevokedWhenNoEnvironmentStartedRunningTheAppMeanwhile(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	lists := 0
	records, _ := racingRecords(t, spec, nil, &lists)

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); len(got) != 0 {
		t.Errorf("the account still holds %q, want nothing", got)
	}
	if topicHeld(server, c, spec.Ref, member) {
		t.Error("the account still publishes to the topic")
	}
	if lists != 2 {
		t.Errorf("the records were listed %d times, want 2", lists)
	}
}

func TestRevokingWhileAnotherEnvironmentIsRecordedReadsTheRecordsOnce(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	before := holdings(server, c, member)
	lists := 0
	records, backing := racingRecords(t, spec, nil, &lists)
	recordApp(t, backing, spec, stackOf("pr-8", "web", "r2"))

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if lists != 1 {
		t.Errorf("the records were listed %d times, want once", lists)
	}
	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q, want %q unchanged", got, before)
	}
}

func TestRevokingAnAppsGrantsLeavesARoleAnOperatorAddedToItsAccount(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedAppAccount(t, server)
	conditioned := &cloudresourcemanager.Expr{Expression: `resource.name == "projects/acme-prod/datasets/sales"`}
	server.project.Bindings = append(server.project.Bindings,
		&cloudresourcemanager.Binding{Role: "roles/bigquery.dataViewer", Members: []string{member}},
		&cloudresourcemanager.Binding{Role: "roles/bigquery.dataEditor", Condition: conditioned, Members: []string{member}},
	)

	if err := revokeUnusedAppAccount(context.Background(), c, recordedStacks{names: []naming.StackName{spec.Ref.Name}}, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	want := []string{
		"roles/bigquery.dataEditor " + member + " " + conditioned.Expression,
		"roles/bigquery.dataViewer " + member + " ",
	}
	if got := projectBindingsOf(server); !slices.Equal(got, want) {
		t.Errorf("the project binds %q, want exactly %q: only the roles ocel granted the account are taken back", got, want)
	}
}

func TestAStackThatStillRecordsComputeAfterARemovalKeepsItsAppGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	records := fake.NewKeyValues()
	ref := provider.StackRef{Tier: environment.TierProduction, Project: "shop", Name: stackOf(stackrecords.ProductionEnv, "web", "r1")}
	recorded := stackrecords.Stack{Kind: provider.StackApp, App: "web", Functions: []provider.Function{{Name: "web"}, {Name: "web-image"}}}
	if err := stackrecords.Write(ctx, records, ref.Tier, ref.Project, ref.Name, recorded); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		going []string
		want  bool
	}{
		{going: []string{"web-image"}, want: true},
		{going: []string{"web"}, want: true},
		{going: []string{"web", "web-image"}, want: false},
	} {
		kept, err := stackKeepsRunning(ctx, records, ref, test.going, nil)
		if err != nil {
			t.Fatalf("stackKeepsRunning(%v) = %v", test.going, err)
		}
		if kept != test.want {
			t.Errorf("stackKeepsRunning(%v) = %v, want %v", test.going, kept, test.want)
		}
	}
}

func TestDestroyingAStackOfSeveralServicesRevokesItsAppsGrants(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	spec := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one")
	spec.App.Functions = append(spec.App.Functions, provider.FunctionSpec{
		Name:      "fn--web--worker",
		Image:     "europe-west1-docker.pkg.dev/acme/ocel/web-worker@sha256:one",
		Framework: spec.App.Functions[0].Framework,
	})
	result, err := released.stacks.Provision(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("Provision(%s) = %v", spec.Ref.Name, err)
	}
	if len(result.Functions) != 2 {
		t.Fatalf("Provision(%s) deployed %+v, want the two functions its spec names", spec.Ref.Name, result.Functions)
	}
	if err := stackrecords.Write(context.Background(), released.store, spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name,
		stackrecords.Stack{Kind: provider.StackApp, App: "web", Functions: result.Functions}); err != nil {
		t.Fatal(err)
	}
	member := "serviceAccount:" + p.resolved.AppAccountEmail(environment.TierProduction, "shop", "web")
	holding := func() bool {
		return slices.ContainsFunc(projectBindingsOf(server.identities()), func(bound string) bool { return strings.Contains(bound, member) })
	}
	if !holding() {
		t.Fatalf("the project binds %q before the destroy, want the app's account in it", projectBindingsOf(server.identities()))
	}

	released.destroy(t, spec.Ref)

	if holding() {
		t.Errorf("the project still binds %q after the app's only stack was destroyed", projectBindingsOf(server.identities()))
	}
}

func TestRemovingTheLastEnvironmentRunningANextAppTakesItOffTheRefreshAccount(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedRefreshOnlyAppAccount(t, server)
	if got := holdings(server, c, member); !slices.Contains(got, "refresh account "+runAsRole) || !slices.Contains(got, "queue "+queueEnqueuerRole) {
		t.Fatalf("the account holds %q before removal, want the refresh account and the enqueuer", got)
	}
	records := recordedStacks{names: []naming.StackName{spec.Ref.Name}}

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); len(got) != 0 {
		t.Errorf("the account still holds %q after the last environment running its app was removed, want nothing", got)
	}
}

func TestAnEnvironmentRecordedWhileANextAppsRefreshGrantsAreRevokedKeepsThem(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c, spec, member := grantedRefreshOnlyAppAccount(t, server)
	before := holdings(server, c, member)
	lists := 0
	var backing keyvalue.Store
	records, backing := racingRecords(t, spec, func() {
		recordApp(t, backing, spec, stackOf("pr-8", "web", "r2"))
	}, &lists)

	if err := revokeUnusedAppAccount(context.Background(), c, records, spec.Ref, nil); err != nil {
		t.Fatalf("revokeUnusedAppAccount() = %v", err)
	}

	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q, want the %q it held before pr-8 started running the app", got, before)
	}
}
