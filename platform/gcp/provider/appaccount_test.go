package gcp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/api/iam/v1"
	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func appAccountsOnly() *iamServer {
	server := grantedIAM()
	server.accounts = map[string]bool{}
	return server
}

func ensuringAccounts(t *testing.T, server *iamServer) (*Provider, *clients) {
	t.Helper()
	c := server.open(t)
	return pushing(t, c.endpoint), c
}

func appNamed(app string, spec provider.StackSpec) provider.StackSpec {
	spec.Ref.Name.App = app
	spec.App.App = app
	return spec
}

func reachingTopics(spec provider.StackSpec) provider.StackSpec {
	spec.App.Values.Bindings = []provider.Binding{{Type: provider.BindingTopic, Name: "topic--resize"}}
	return spec
}

func policyOrEmpty(policy *iam.Policy) []*iam.Binding {
	if policy == nil {
		return nil
	}
	return policy.Bindings
}

func topicBindingsOf(policy *pubsub.Policy) []*pubsub.Binding {
	if policy == nil {
		return nil
	}
	return policy.Bindings
}

func projectBindingsOf(server *iamServer) []string {
	server.mu.Lock()
	defer server.mu.Unlock()
	var bound []string
	for _, binding := range server.project.Bindings {
		condition := ""
		if binding.Condition != nil {
			condition = binding.Condition.Expression
		}
		for _, member := range binding.Members {
			bound = append(bound, binding.Role+" "+member+" "+condition)
		}
	}
	slices.Sort(bound)
	return bound
}

func TestTwoAppsInOneProjectRunAsDifferentAccounts(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	spec := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})

	web, err := p.ensureAppAccount(context.Background(), c, appNamed("web", spec), nil)
	if err != nil {
		t.Fatalf("ensureAppAccount(web) = %v", err)
	}
	api, err := p.ensureAppAccount(context.Background(), c, appNamed("api", spec), nil)
	if err != nil {
		t.Fatalf("ensureAppAccount(api) = %v", err)
	}

	if web == api || web != c.AppAccountEmail(environment.TierProduction, "shop", "web") || api != c.AppAccountEmail(environment.TierProduction, "shop", "api") {
		t.Errorf("web runs as %q and api as %q, want an account each named for its own app", web, api)
	}
	if len(server.created) != 2 {
		t.Errorf("%d accounts were created, want one per app", len(server.created))
	}
}

func TestAFunctionServiceRunsAsItsAppsOwnAccountNotTheTiers(t *testing.T) {
	t.Parallel()
	function := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	worker := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	worker.App.Workers = []provider.WorkerSpec{{Name: "media"}}

	for name, deploy := range map[string]func(*Provider, provider.StackSpec) error{
		"a function": func(p *Provider, spec provider.StackSpec) error {
			_, err := p.ProvisionFunctions(context.Background(), spec, nil)
			return err
		},
		"a container": func(p *Provider, spec provider.StackSpec) error {
			_, err := p.ProvisionContainers(context.Background(), containerStackDeclaring(spec.Ref.Tier, "production", provider.AppValues{}), nil)
			return err
		},
		"a worker": func(p *Provider, spec provider.StackSpec) error {
			account, err := p.ensureAppAccount(context.Background(), p.resolved, worker, nil)
			if err != nil {
				return err
			}
			_, err = p.provisionWorkers(context.Background(), p.resolved, worker, "europe-west1-docker.pkg.dev/acme/ocel/api@sha256:abc", account, nil, nil, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := &runServer{iam: appAccountsOnly()}
			p := server.open(t)

			if err := deploy(p, function); err != nil {
				t.Fatalf("deploying %s = %v", name, err)
			}
			want := p.resolved.AppAccountEmail(environment.TierProduction, "shop", "api")
			if len(server.created) == 0 {
				t.Fatalf("%s created no service", name)
			}
			for _, service := range server.created {
				if got := service.Template.ServiceAccount; got != want {
					t.Errorf("%s runs as %q, want its app's own %q", service.Name, got, want)
				}
			}
		})
	}
}

func TestAnAppsAccountIsCreatedOnceAndReusedByTheNextDeploy(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	spec := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	if _, err := p.ensureAppAccount(context.Background(), c, spec, nil); err != nil {
		t.Fatal(err)
	}
	created, written := len(server.created), server.writes

	if _, err := p.ensureAppAccount(context.Background(), c, spec, nil); err != nil {
		t.Fatalf("ensureAppAccount() again = %v", err)
	}

	if len(server.created) != created || server.writes != written {
		t.Errorf("a second deploy created %d accounts and wrote %d policies, want none: the account and its grants stand", len(server.created)-created, server.writes-written)
	}
}

func TestANewAccountMayReadItsRecordsAndOpenUnderItsTiersKeyAndNothingMore(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	email, err := p.ensureAppAccount(context.Background(), c, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), nil)
	if err != nil {
		t.Fatal(err)
	}

	member := "serviceAccount:" + email
	want := []string{
		"roles/cloudkms.cryptoKeyDecrypter " + member + ` resource.name == "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"`,
		"roles/datastore.viewer " + member + ` resource.name == "projects/acme-prod/databases/ocel"`,
	}
	if got := projectBindingsOf(server); !slices.Equal(got, want) {
		t.Errorf("the project binds %q, want exactly %q", got, want)
	}
	if len(server.keyPolicy) != 0 || server.queueWrites != 0 || server.topicWrites != 0 {
		t.Errorf("the key policy, queue and topics were written (%d, %d, %d), and an app that reaches no topics needs none of them", len(server.keyPolicy), server.queueWrites, server.topicWrites)
	}
	if got := server.created[0]; got.AccountId != strings.TrimSuffix(email, "@acme-prod.iam.gserviceaccount.com") ||
		got.ServiceAccount.DisplayName != "ocel shop/api (production)" ||
		got.ServiceAccount.Description != "the identity app api of project shop runs as in the production tier" {
		t.Errorf("the account was created as %+v, want one named for its app, project and tier", got)
	}
}

func TestAGrantRefusedBecauseTheNewAccountIsNotYetVisibleIsRetried(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.unseen = 2
	p, c := ensuringAccounts(t, server)

	if _, err := p.ensureAppAccount(context.Background(), c, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), nil); err != nil {
		t.Fatalf("ensureAppAccount() = %v, want the grant retried until the account is visible", err)
	}

	if server.projectAttempts != 4 || server.projectWrites != 2 {
		t.Errorf("the project policy was written %d times and %d took, want 2 refused then 2 granted", server.projectAttempts, server.projectWrites)
	}
}

func TestAProjectWithNoRoomForAnotherAccountIsRefusedNamingTheQuota(t *testing.T) {
	t.Parallel()
	for name, configure := range map[string]func(*iamServer){
		"by message":  func(server *iamServer) { server.quotaFull = true },
		"by throttle": func(server *iamServer) { server.quotaThrottled = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := appAccountsOnly()
			configure(server)
			p, c := ensuringAccounts(t, server)

			_, err := p.ensureAppAccount(context.Background(), c, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), nil)

			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(refused.Message, "Service Account Count") {
				t.Errorf("ensureAppAccount() = %v, want a %s refusal naming the Service Account Count quota", err, refusal.CodeNotReady)
			}
		})
	}
}

func TestAnAppReachingTopicsMayRecordRunsDelayMessagesAsItselfAndPublishToItsTopics(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.accountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	declared := map[string]*provider.TopicSpec{"resize": {}}

	email, err := p.ensureAppAccount(context.Background(), c, spec, declared)
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}

	member := "serviceAccount:" + email
	if !slices.Contains(projectBindingsOf(server), "roles/datastore.user "+member+` resource.name == "projects/acme-prod/databases/ocel-production-tasks"`) {
		t.Errorf("the project binds %q, want the app to write the production task database", projectBindingsOf(server))
	}
	queue := c.DelayQueuePath("europe-west1", environment.TierProduction)
	var queueRoles []string
	for _, binding := range server.queuePolicies[queue].GetBindings() {
		if slices.Contains(binding.GetMembers(), member) {
			queueRoles = append(queueRoles, binding.GetRole())
		}
	}
	slices.Sort(queueRoles)
	if !slices.Equal(queueRoles, []string{"roles/cloudtasks.enqueuer", "roles/cloudtasks.taskDeleter"}) {
		t.Errorf("the app holds %v on the delay queue, want enqueuer and taskDeleter", queueRoles)
	}
	own := "/v1/projects/acme-prod/serviceAccounts/" + email
	var actingAs []string
	for _, binding := range policyOrEmpty(server.accountPolicies[own]) {
		if binding.Role == runAsRole {
			actingAs = append(actingAs, binding.Members...)
		}
	}
	slices.Sort(actingAs)
	wantActingAs := []string{member, "serviceAccount:service-123456789@gcp-sa-cloudtasks.iam.gserviceaccount.com"}
	slices.Sort(wantActingAs)
	if !slices.Equal(actingAs, wantActingAs) {
		t.Errorf("the app's own account may be acted as by %v, want exactly %v: it enqueues tasks signed as itself, and Cloud Tasks must act as it to sign them", actingAs, wantActingAs)
	}
	tierAccount := "/v1/projects/acme-prod/serviceAccounts/ocel-production@acme-prod.iam.gserviceaccount.com"
	if _, touched := server.accountPolicies[tierAccount]; touched {
		t.Errorf("the policy of %s was written, and no account but the app's own is acted as", tierAccount)
	}
	topic := "/v1/projects/acme-prod/topics/" + taskNames(c.Names, spec.Ref).Topic("resize")
	published := false
	for _, binding := range topicBindingsOf(server.topicPolicies[topic]) {
		published = published || binding.Role == "roles/pubsub.publisher" && slices.Contains(binding.Members, member)
	}
	if !published {
		t.Errorf("topic %s has policy %+v, want the app to publish to it", topic, server.topicPolicies[topic])
	}
}

func TestAnAppReachingNoTopicsIsGrantedNothingOnTheQueueOrItsOwnAccount(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.accountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)

	if _, err := p.ensureAppAccount(context.Background(), c, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), nil); err != nil {
		t.Fatal(err)
	}

	if server.queueWrites != 0 || server.accountWrites != 0 || server.topicWrites != 0 {
		t.Errorf("an app with no topics wrote %d queue, %d account and %d topic policies, want none", server.queueWrites, server.accountWrites, server.topicWrites)
	}
	if slices.ContainsFunc(projectBindingsOf(server), func(bound string) bool { return strings.HasPrefix(bound, "roles/datastore.user ") }) {
		t.Errorf("the project binds %q, and an app with no topics writes no task database", projectBindingsOf(server))
	}
}

func TestAQueueGrantRacedByAnotherDeployIsReadAgainAndWritten(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.queueAborts = 2
	c := server.open(t)

	if err := c.bindQueueRoles(context.Background(), environment.TierProduction, "serviceAccount:app@acme-prod.iam.gserviceaccount.com", queueRoles); err != nil {
		t.Fatalf("bindQueueRoles() = %v", err)
	}

	if server.queueWrites != 1 {
		t.Errorf("%d queue policies were written, want the one that took after two aborted", server.queueWrites)
	}
}

func TestADeployCredentialWithoutTheAppAccountsRoleIsToldWhichRoleItLacks(t *testing.T) {
	t.Parallel()
	for name, deniedAt := range map[string]string{"reading": http.MethodGet, "creating": http.MethodPost} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := (&iamServer{}).serve(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == deniedAt || r.Method == http.MethodGet {
					status := http.StatusNotFound
					if r.Method == deniedAt {
						status = http.StatusForbidden
					}
					w.WriteHeader(status)
					w.Write([]byte(`{"error":{"code":` + strconv.Itoa(status) + `,"message":"denied"}}`))
					return
				}
				t.Errorf("unexpected %s %s", r.Method, r.URL)
			})

			err := c.createAppAccount(context.Background(), environment.TierProduction, "shop", "api")

			if err == nil || !strings.Contains(err.Error(), "projects/acme-prod/roles/ocel_app_accounts") {
				t.Errorf("createAppAccount() = %v, want an error naming projects/acme-prod/roles/ocel_app_accounts", err)
			}
		})
	}
}

func TestAnAppsOwnAccountNotYetReadableIsActedAsOnceItIs(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.accountPolicies = map[string]*iam.Policy{}
	server.accountPolicyUnseen = 2
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	email, err := p.ensureAppAccount(context.Background(), c, spec, map[string]*provider.TopicSpec{"resize": {}})
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v, want the grant retried until the account is readable", err)
	}

	member := "serviceAccount:" + email
	bound := false
	for _, binding := range policyOrEmpty(server.accountPolicies["/v1/projects/acme-prod/serviceAccounts/"+email]) {
		bound = bound || binding.Role == runAsRole && slices.Contains(binding.Members, member)
	}
	if !bound {
		t.Errorf("the app's own account is bound to %+v, want the app to act as it", server.accountPolicies)
	}
}

func TestAnAppsOwnAccountItMayNotActAsNamesTheCustomRole(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.accountPolicies = map[string]*iam.Policy{}
	server.accountPolicyDenied = true
	p, c := ensuringAccounts(t, server)
	spec := reachingTopics(functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))

	_, err := p.ensureAppAccount(context.Background(), c, spec, map[string]*provider.TopicSpec{"resize": {}})

	if err == nil || !strings.Contains(err.Error(), "projects/acme-prod/roles/ocel_app_accounts") {
		t.Errorf("ensureAppAccount() = %v, want an error naming projects/acme-prod/roles/ocel_app_accounts", err)
	}
}
