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
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

func releaseOf(id string) naming.ReleaseToken { return naming.NewReleaseToken(id, "fingerprint") }

func stackOf(env, app, release string) naming.StackName {
	return naming.AppStack(env, app, releaseOf(release))
}

func appMember(c *gcloud.Clients, ref provider.StackRef, app string) string {
	return "serviceAccount:" + c.AppAccountEmail(ref.Project, ref.Name.Env, app)
}

func applyAppGrants(t *testing.T, c *gcloud.Clients, spec provider.StackSpec) error {
	t.Helper()
	ref := spec.Ref
	account := c.AppAccount(ref.Project, ref.Name.Env, spec.App.App)
	want, err := release.AppGrants(context.Background(), c, component.Registry{bucket.New(nil, nil).Component()}, cacheGrants, spec, account, appMember(c, ref, spec.App.App), nil, nil)
	if err != nil {
		return err
	}
	return grants.Apply(context.Background(), c, ref.Tier, want, nil)
}

func grantedAppAccount(t *testing.T, server *googletest.IAM) (*Provider, *gcloud.Clients, provider.StackSpec, string) {
	t.Helper()
	return grantedAccountOf(t, server, reachingTopics(routedNextSpec()), map[string]*provider.TopicSpec{"resize": {}})
}

func grantedAccountOf(t *testing.T, server *googletest.IAM, spec provider.StackSpec, declared map[string]*provider.TopicSpec) (*Provider, *gcloud.Clients, provider.StackSpec, string) {
	t.Helper()
	server.AccountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	recordApp(t, p, spec.Ref, spec.Ref.Name)
	email, _, err := ensureAccount(p, context.Background(), c, spec, declared)
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}
	return p, c, spec, "serviceAccount:" + email
}

func recordApp(t *testing.T, p *Provider, ref provider.StackRef, name naming.StackName) {
	t.Helper()
	if err := stackrecords.Write(context.Background(), p.KeyValues(), ref.Tier, ref.Project, name, stackrecords.Stack{Kind: provider.StackApp, App: name.App}); err != nil {
		t.Fatal(err)
	}
}

func removeStack(t *testing.T, p *Provider, c *gcloud.Clients, ref provider.StackRef) {
	t.Helper()
	if err := p.release().DeleteUnusedAccount(context.Background(), c, ref, nil, nil, nil); err != nil {
		t.Fatalf("revokeAfterRemoval(%s) = %v", ref.Name, err)
	}
}

func holdings(server *googletest.IAM, c *gcloud.Clients, member string) []string {
	var held []string
	for _, bound := range slices.Concat(projectBindingsOf(server), bucketBindingsOf(server), keyBindingsOf(server)) {
		if strings.Contains(bound, member) {
			held = append(held, bound)
		}
	}
	queue := c.DelayQueuePath("europe-west1", environment.TierProduction)
	for _, binding := range server.QueuePolicies[queue].GetBindings() {
		if slices.Contains(binding.GetMembers(), member) {
			held = append(held, "queue "+binding.GetRole())
		}
	}
	own := "/v1/projects/acme-prod/serviceAccounts/" + strings.TrimPrefix(member, "serviceAccount:")
	for _, binding := range policyOrEmpty(server.AccountPolicies[own]) {
		if slices.Contains(binding.Members, member) {
			held = append(held, "own account "+binding.Role)
		}
	}
	return held
}

func resizePath(t *testing.T, c *gcloud.Clients, spec provider.StackSpec) string {
	return "/v1/projects/acme-prod/topics/" + topicNamed(t, topic.Names(c.Names, spec.Ref), "resize")
}

func publishersOf(server *googletest.IAM, path string) []string {
	server.Mu.Lock()
	defer server.Mu.Unlock()
	for _, binding := range topicBindingsOf(server.TopicPolicies[path]) {
		if binding.Role == "roles/pubsub.publisher" {
			return binding.Members
		}
	}
	return nil
}

func TestEachEnvironmentOfAnAppRunsAsAnAccountOfItsOwn(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	seven := functionStackDeclaring(environment.TierPreview, "pr-7", provider.AppValues{})
	eight := functionStackDeclaring(environment.TierPreview, "pr-8", provider.AppValues{})

	first, _, err := ensureAccount(p, context.Background(), c, seven, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount(pr-7) = %v", err)
	}
	second, _, err := ensureAccount(p, context.Background(), c, eight, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount(pr-8) = %v", err)
	}

	if first == second {
		t.Errorf("pr-7 and pr-8 both run as %s, and a preview's code then holds every grant another preview's resources gave it", first)
	}
	if len(server.Created) != 2 {
		t.Errorf("%d accounts were created, want one per environment", len(server.Created))
	}
}

func TestAPreviewsAccountHoldsNothingOnAnotherPreviewsBucket(t *testing.T) {
	t.Parallel()
	p, c, _, buckets := servingAccountsAndBuckets(t)
	sevens, eights := appBucketOf(t, c.Names, "pr-7", "uploads"), appBucketOf(t, c.Names, "pr-8", "uploads")
	existingBuckets(buckets, sevens, eights)
	seven := functionStackDeclaring(environment.TierPreview, "pr-7", provider.AppValues{})
	seven.App.Values.Bindings = []provider.Binding{{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: sevens}}}
	eight := functionStackDeclaring(environment.TierPreview, "pr-8", provider.AppValues{})
	eight.App.Values.Bindings = []provider.Binding{{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: eights}}}

	sevenAccount, _, err := ensureAccount(p, context.Background(), c, seven, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount(pr-7) = %v", err)
	}
	if _, _, err := ensureAccount(p, context.Background(), c, eight, nil); err != nil {
		t.Fatalf("ensureAppAccount(pr-8) = %v", err)
	}

	if got := rolesOf(buckets.granted(eights), "serviceAccount:"+sevenAccount); got != nil {
		t.Errorf("pr-7's code runs as an account holding %v on pr-8's bucket %s", got, eights)
	}
}

func TestADeployThatStopsBindingABucketOfAnotherEnvironmentTakesTheGrantOnItAway(t *testing.T) {
	t.Parallel()
	p, c, _, buckets := servingAccountsAndBuckets(t)
	shared := appBucketOf(t, c.Names, "production", "media")
	existingBuckets(buckets, shared)
	spec := functionStackDeclaring(environment.TierPreview, "pr-7", provider.AppValues{})
	spec.App.Grants = []provider.Binding{{Type: provider.BindingBucket, Name: "media", Properties: map[string]string{provider.PropertyBucket: shared}}}
	account, _, err := ensureAccount(p, context.Background(), c, spec, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}
	if got := rolesOf(buckets.granted(shared), "serviceAccount:"+account); len(got) != 1 {
		t.Fatalf("the account holds %v on %s, want the grant the deploy asked for", got, shared)
	}
	spec.App.Grants = nil

	if _, _, err := ensureAccount(p, context.Background(), c, spec, nil); err != nil {
		t.Fatalf("ensureAppAccount() without the grant = %v", err)
	}

	if got := rolesOf(buckets.granted(shared), "serviceAccount:"+account); got != nil {
		t.Errorf("the account still holds %v on %s, a bucket no infra stack of pr-7 records, after a deploy that no longer binds it", got, shared)
	}
}

func TestADeployRecordsWhatItGrantsBeforeItGrantsIt(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.AccountPolicies = map[string]*iam.Policy{}
	server.AccountPolicyDenied = true
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	if _, _, err := ensureAccount(p, context.Background(), c, spec, nil); err == nil {
		t.Fatal("ensureAppAccount() over a denied account policy = nil, want the refusal")
	}

	recorded, err := grants.Read(context.Background(), p.KeyValues(), spec.Ref.Tier, c.AppAccount("shop", "production", "api"))
	if err != nil {
		t.Fatal(err)
	}
	member := appMember(c, spec.Ref, "api")
	if !slices.ContainsFunc(recorded, func(grant grants.Grant) bool { return grant.Member == member && grant.Role == credential.RecordsRole }) {
		t.Errorf("the deploy granted the records role and recorded %+v, want every grant on record before it lands so a removal can take it back", recorded)
	}
}

func TestRemovingTheLastReleaseOfAnEnvironmentRevokesEveryGrantItsAccountHeld(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, member := grantedAppAccount(t, server)
	if got := holdings(server, c, member); len(got) != 9 {
		t.Fatalf("the account holds %q before removal, want the records, opening and sealing on the key, runs and tag records, cache in the edge and cache buckets, assets, queue and own account grants", got)
	}
	if !slices.Contains(publishersOf(server, resizePath(t, c, spec)), member) {
		t.Fatalf("resize has publishers %q before removal, want the account in them", publishersOf(server, resizePath(t, c, spec)))
	}

	removeStack(t, p, c, spec.Ref)

	if got := holdings(server, c, member); len(got) != 0 {
		t.Errorf("the account still holds %q after its environment's last release was removed, want nothing", got)
	}
	if got := publishersOf(server, resizePath(t, c, spec)); slices.Contains(got, member) {
		t.Errorf("resize still has publishers %q, want the account gone", got)
	}
	account := c.AppAccount(spec.Ref.Project, spec.Ref.Name.Env, "web")
	if recorded, err := grants.Read(context.Background(), p.KeyValues(), spec.Ref.Tier, account); err != nil || recorded != nil {
		t.Errorf("readAppGrants() after the removal = %+v, %v, want nothing on record", recorded, err)
	}
}

func accountPathOf(c *gcloud.Clients, ref provider.StackRef, app string) string {
	return "/v1/projects/acme-prod/serviceAccounts/" + c.AppAccountEmail(ref.Project, ref.Name.Env, app)
}

func TestRemovingTheLastReleaseOfAnEnvironmentDeletesItsAccount(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, _ := grantedAppAccount(t, server)

	removeStack(t, p, c, spec.Ref)

	if want := []string{accountPathOf(c, spec.Ref, "web")}; !slices.Equal(server.DeletedAccounts, want) {
		t.Errorf("deleted the accounts %q, want %q: every removed environment that keeps its account takes one more of the project's service account quota", server.DeletedAccounts, want)
	}
}

func TestRemovingAnEnvironmentWhoseAccountIsAlreadyGoneSucceeds(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, _ := grantedAppAccount(t, server)
	server.Mu.Lock()
	delete(server.Accounts, c.AppAccount(spec.Ref.Project, spec.Ref.Name.Env, "web"))
	server.Mu.Unlock()

	removeStack(t, p, c, spec.Ref)
}

func TestRemovingAnEnvironmentKeepsItsAccountWhenADeployRecordsTheAppBeforeTheDelete(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, _ := grantedAppAccount(t, server)
	removal := p.release()
	recorded := &countingRecords{Store: removal.Records}
	recorded.after = func(listed int) {
		if listed == 1 {
			recordApp(t, p, spec.Ref, stackOf(spec.Ref.Name.Env, "web", "r2"))
		}
	}
	removal.Records = recorded

	if err := removal.DeleteUnusedAccount(context.Background(), c, spec.Ref, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	if len(server.DeletedAccounts) != 0 {
		t.Errorf("deleted the accounts %q, want none: a deploy recorded the app after the removal first read the records, and its service would run as a deleted account", server.DeletedAccounts)
	}
}

func TestRedeployingAnEnvironmentAfterItsRemovalCreatesAndGrantsItsAccountAgain(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, member := grantedAppAccount(t, server)
	before := holdings(server, c, member)
	removeStack(t, p, c, spec.Ref)

	if _, _, err := ensureAccount(p, context.Background(), c, spec, map[string]*provider.TopicSpec{"resize": {}}); err != nil {
		t.Fatalf("ensureAppAccount() after the removal = %v", err)
	}

	if len(server.Created) != 2 {
		t.Errorf("%d accounts were created, want the account made again after its removal deleted it", len(server.Created))
	}
	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q after the redeploy, want the %q it held before the removal", got, before)
	}
}

func TestRemovingOneReleaseLeavesTheAccountAnotherReleaseOfTheEnvironmentRuns(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, member := grantedAppAccount(t, server)
	before := holdings(server, c, member)
	older := spec.Ref
	older.Name = stackOf(spec.Ref.Name.Env, "web", "r0")
	recordApp(t, p, spec.Ref, older.Name)

	removeStack(t, p, c, older)

	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q, want the %q it held while another release of the app is recorded", got, before)
	}
	if len(server.DeletedAccounts) != 0 {
		t.Errorf("deleted the accounts %q, want none while another release of the app is recorded", server.DeletedAccounts)
	}
}

func TestRemovingOneEnvironmentLeavesAnotherEnvironmentsAccountAlone(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, _ := grantedAppAccount(t, server)
	other := spec
	other.Ref.Name = stackOf("pr-8", "web", "r1")
	recordApp(t, p, other.Ref, other.Ref.Name)
	otherMember, _, err := ensureAccount(p, context.Background(), c, other, map[string]*provider.TopicSpec{"resize": {}})
	if err != nil {
		t.Fatal(err)
	}
	before := holdings(server, c, "serviceAccount:"+otherMember)

	removeStack(t, p, c, spec.Ref)

	if got := holdings(server, c, "serviceAccount:"+otherMember); !slices.Equal(got, before) {
		t.Errorf("pr-8's account holds %q, want the %q it held: removing production touches only production's account", got, before)
	}
}

func TestRevokingAnAppsGrantsTouchesNoOtherMembersBindings(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, member := grantedAppAccount(t, server)
	other := "serviceAccount:other@acme-prod.iam.gserviceaccount.com"
	for _, binding := range server.Project.Bindings {
		if binding.Role == credential.RecordsRole {
			binding.Members = append(binding.Members, other)
		}
	}
	conditioned := &cloudresourcemanager.Expr{Expression: `resource.name == "projects/acme-prod/datasets/sales"`}
	server.Project.Bindings = append(server.Project.Bindings,
		&cloudresourcemanager.Binding{Role: "roles/viewer", Members: []string{other}},
		&cloudresourcemanager.Binding{Role: "roles/bigquery.dataEditor", Condition: conditioned, Members: []string{member}},
	)

	removeStack(t, p, c, spec.Ref)

	want := []string{
		"roles/bigquery.dataEditor " + member + " " + conditioned.Expression,
		credential.RecordsRole + " " + other + ` resource.name == "projects/acme-prod/databases/` + ports.SharedDatabase("ocel", environment.TierProduction) + `"`,
		"roles/viewer " + other + " ",
	}
	if got := projectBindingsOf(server); !slices.Equal(got, want) {
		t.Errorf("the project binds %q, want exactly %q: only what ocel granted the account is taken back and every binding left empty is dropped", got, want)
	}
	if server.Project.Version != policy.ConditionalVersion {
		t.Errorf("the policy was written at version %d, want %d: its bindings are conditional", server.Project.Version, policy.ConditionalVersion)
	}
}

func TestRevokingAnAppsGrantsSkipsWhatIsAlreadyGone(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, member := grantedAppAccount(t, server)
	server.TasksAbsent = true
	server.DeletedTopics = map[string]bool{resizePath(t, c, spec): true}

	removeStack(t, p, c, spec.Ref)

	if got := projectBindingsOf(server); slices.ContainsFunc(got, func(bound string) bool { return strings.Contains(bound, member) }) {
		t.Errorf("the project still binds %q, want the member gone from it", got)
	}
}

func TestADeployThatStopsReachingATopicTakesItsPublisherGrantAway(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, member := grantedAppAccount(t, server)
	server.TopicPolicies[resizePath(t, c, spec)].Bindings = append(server.TopicPolicies[resizePath(t, c, spec)].Bindings,
		&pubsub.Binding{Role: "roles/pubsub.publisher", Members: []string{"serviceAccount:other@acme-prod.iam.gserviceaccount.com"}})

	if _, _, err := ensureAccount(p, context.Background(), c, spec, map[string]*provider.TopicSpec{}); err != nil {
		t.Fatalf("ensureAppAccount() without the topic = %v", err)
	}

	if got := publishersOf(server, resizePath(t, c, spec)); slices.Contains(got, member) || len(got) != 1 {
		t.Errorf("resize has publishers %q, want the other member alone once the app no longer declares the topic", got)
	}
}

func TestRemovingAnInfraStackRevokesNoAppAccount(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c, spec, member := grantedAppAccount(t, server)
	before := holdings(server, c, member)
	infra := spec.Ref
	infra.Name = naming.InfraStack(stackrecords.ProductionEnv)

	removeStack(t, p, c, infra)

	if got := holdings(server, c, member); !slices.Equal(got, before) {
		t.Errorf("the account holds %q, want the %q it held: an infra stack runs no app", got, before)
	}
}

func TestDestroyingTheLastReleaseOfAnAppRevokesItsAccountAndAnotherReleaseKeepsIt(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	released := newReleasedStacks(p)
	first := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one")
	second := functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:two")
	first.App.Values, second.App.Values = openingASecret, openingASecret
	released.provision(t, first)
	released.provision(t, second)
	member := appMember(p.resolved, first.Ref, "web")
	holding := func() bool {
		return slices.ContainsFunc(projectBindingsOf(server.Identities()), func(bound string) bool { return strings.Contains(bound, member) })
	}
	if !holding() {
		t.Fatalf("the project binds %q before any destroy, want the app's account in it", projectBindingsOf(server.Identities()))
	}

	released.destroy(t, first.Ref)
	if !holding() {
		t.Error("destroying one release revoked the account while another release of the app is recorded")
	}
	released.destroy(t, second.Ref)
	if holding() {
		t.Errorf("the project still binds %q after the app's last release was destroyed", projectBindingsOf(server.Identities()))
	}
}

func TestAStackThatStillRecordsComputeAfterARemovalKeepsItsAppGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := pushing(t, "")
	ref := provider.StackRef{Tier: environment.TierProduction, Project: "shop", Name: stackOf(stackrecords.ProductionEnv, "web", "r1")}
	recorded := stackrecords.Stack{Kind: provider.StackApp, App: "web", Functions: []provider.Function{{Name: "web"}, {Name: "web-image"}}}
	if err := stackrecords.Write(ctx, p.KeyValues(), ref.Tier, ref.Project, ref.Name, recorded); err != nil {
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
		kept, err := release.StackKeepsRunning(ctx, p.KeyValues(), ref, test.going, nil)
		if err != nil {
			t.Fatalf("stackKeepsRunning(%v) = %v", test.going, err)
		}
		if kept != test.want {
			t.Errorf("stackKeepsRunning(%v) = %v, want %v", test.going, kept, test.want)
		}
	}
}
