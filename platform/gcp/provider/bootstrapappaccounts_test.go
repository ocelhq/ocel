package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type listedAccount struct {
	id          string
	email       string
	description string
}

func (a listedAccount) emailOr(project string) string {
	if a.email != "" {
		return a.email
	}
	return a.id + "@" + project + accountDomain
}

type serviceAccountListing struct {
	mu       sync.Mutex
	pages    [][]listedAccount
	byID     map[string]listedAccount
	refusing map[string]int
	tokens   []string
	deleted  []string
	listed   int
	policy   *cloudresourcemanager.Policy
	events   []string
}

func listing(accounts ...listedAccount) *serviceAccountListing {
	return &serviceAccountListing{pages: [][]listedAccount{accounts}}
}

func (s *serviceAccountListing) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		const collection = "/v1/projects/acme-prod/serviceAccounts"
		switch {
		case r.URL.Path == "/v1/projects/acme-prod:getIamPolicy":
			if s.policy == nil {
				s.policy = &cloudresourcemanager.Policy{Etag: "BwXhoLA="}
			}
			json.NewEncoder(w).Encode(s.policy)
		case r.URL.Path == "/v1/projects/acme-prod:setIamPolicy":
			var asked cloudresourcemanager.SetIamPolicyRequest
			if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			s.policy = asked.Policy
			s.events = append(s.events, "revoke")
			json.NewEncoder(w).Encode(s.policy)
		case r.Method == http.MethodGet && r.URL.Path == collection:
			s.listed++
			token := r.URL.Query().Get("pageToken")
			s.tokens = append(s.tokens, token)
			page := 0
			if token != "" {
				page, _ = strconv.Atoi(strings.TrimPrefix(token, "page-"))
			}
			var answered iam.ListServiceAccountsResponse
			for _, account := range s.pages[page] {
				answered.Accounts = append(answered.Accounts, &iam.ServiceAccount{Email: account.emailOr("acme-prod"), Description: account.description})
			}
			if page+1 < len(s.pages) {
				answered.NextPageToken = "page-" + strconv.Itoa(page+1)
			}
			json.NewEncoder(w).Encode(answered)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, collection+"/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, collection+"/"), "@acme-prod"+accountDomain)
			account, found := s.byID[id]
			if !found {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"not found"}}`))
				return
			}
			json.NewEncoder(w).Encode(&iam.ServiceAccount{Email: account.emailOr("acme-prod"), Description: account.description})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, collection+"/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, collection+"/"), "@acme-prod"+accountDomain)
			if code, refused := s.refusing[id]; refused {
				w.WriteHeader(code)
				w.Write([]byte(`{"error":{"code":` + strconv.Itoa(code) + `,"message":"refused"}}`))
				return
			}
			s.deleted = append(s.deleted, id)
			s.events = append(s.events, "delete "+id)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("the sweep called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *serviceAccountListing) deletes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.deleted)
}

type recordedApp struct {
	tier    environment.Tier
	project string
	stack   naming.StackName
}

func sweepingFor(t *testing.T, s *serviceAccountListing, stacks ...recordedApp) (bootstrap, *fake.Log) {
	t.Helper()
	records := fake.NewKeyValues()
	for _, recorded := range stacks {
		if err := stackrecords.Write(context.Background(), records, recorded.tier, recorded.project, recorded.stack, stackrecords.Stack{}); err != nil {
			t.Fatalf("record %s: %v", recorded.stack, err)
		}
	}
	return bootstrap{clients: (&iamServer{}).serve(t, s.handler(t)), records: records}, &fake.Log{}
}

func appAccountOf(c *clients, tier environment.Tier, project, app string) listedAccount {
	return listedAccount{id: c.AppAccount(tier, project, app), description: appAccountDescription(tier, project, app)}
}

func sweepNames() Names { return Names{namespace: "ocel", project: "acme-prod"} }

func sweepNamed() *clients { return &clients{Names: sweepNames()} }

func sweepRequest(tier environment.Tier) provider.BootstrapRequest {
	return provider.BootstrapRequest{Tier: tier}
}

const deletedOne = "INFO Deleted 1 of the preview tier's app service accounts: no environment runs their apps any more"

func TestABootstrapDeletesTheAccountOfAnAppNoEnvironmentOfItsTierRuns(t *testing.T) {
	t.Parallel()
	unused := appAccountOf(sweepNamed(), environment.TierPreview, "shop", "web")
	server := listing(unused)
	b, log := sweepingFor(t, server)

	if err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); !slices.Equal(got, []string{unused.id}) {
		t.Errorf("deleted %v, want only %s", got, unused.id)
	}
	if !slices.Contains(log.Lines(), deletedOne) {
		t.Errorf("progress = %q, want the count said", log.Lines())
	}
}

func TestABootstrapKeepsTheAccountOfAnAppAnotherEnvironmentStillRuns(t *testing.T) {
	t.Parallel()
	running := appAccountOf(sweepNamed(), environment.TierPreview, "shop", "web")
	server := listing(running)
	b, log := sweepingFor(t, server, recordedApp{environment.TierPreview, "shop", stackOf("pr-8", "web", "r1")})

	if err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); len(got) != 0 {
		t.Errorf("deleted %v, want nothing: app web still runs in pr-8", got)
	}
	if !slices.Contains(log.Lines(), "DEBUG No app service account of the preview tier is unused") {
		t.Errorf("progress = %q, want the nothing-unused line", log.Lines())
	}
}

func TestABootstrapDeletesTheAccountOfAnAppWhoseProjectOnlyHoldsOtherAppsAndInfrastructure(t *testing.T) {
	t.Parallel()
	unused := appAccountOf(sweepNamed(), environment.TierPreview, "shop", "web")
	server := listing(unused)
	b, log := sweepingFor(t, server,
		recordedApp{environment.TierPreview, "shop", naming.InfraStack("pr-8")},
		recordedApp{environment.TierPreview, "shop", stackOf("pr-8", "api", "r1")})

	if err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); !slices.Equal(got, []string{unused.id}) {
		t.Errorf("deleted %v, want %s: only app api and the infrastructure are recorded", got, unused.id)
	}
}

func TestABootstrapKeepsEveryTierAccountWhateverItsDescriptionSays(t *testing.T) {
	t.Parallel()
	names := sweepNamed()
	held := bootstrap{clients: names}
	forged := appAccountDescription(environment.TierPreview, "shop", "web")
	var accounts []listedAccount
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		for _, id := range []string{names.PushAccount(tier), names.RealtimeAccount(tier), names.EnvSourceSyncAccount(tier)} {
			accounts = append(accounts,
				listedAccount{id: id, description: held.purposeOf(tier, id).description},
				listedAccount{id: id, description: forged})
		}
	}
	accounts = append(accounts,
		listedAccount{id: names.Connector(), description: "the connector"},
		listedAccount{id: names.Connector(), description: forged})
	server := listing(accounts...)
	b, log := sweepingFor(t, server)

	if err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); len(got) != 0 {
		t.Errorf("deleted %v, want no tier account", got)
	}
}

func TestABootstrapKeepsAccountsOfAnotherTierAnotherNamespaceOrAnotherMaker(t *testing.T) {
	t.Parallel()
	names := sweepNamed()
	otherTier := appAccountOf(names, environment.TierProduction, "shop", "web")
	own := appAccountOf(names, environment.TierPreview, "shop", "web")
	otherNamespace := listedAccount{id: "other" + strings.TrimPrefix(own.id, "ocel"), description: own.description}
	forged := listedAccount{id: "ocel-0123456789", description: own.description}
	otherProject := listedAccount{id: own.id, email: own.id + "@elsewhere" + accountDomain, description: own.description}
	server := listing(otherTier, otherNamespace, forged, otherProject)
	b, log := sweepingFor(t, server)

	if err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); len(got) != 0 {
		t.Errorf("deleted %v, want nothing: none is an app account of the preview tier of this namespace and project", got)
	}
}

func TestABootstrapFindsAnUnusedAppAccountOnALaterPageOfTheListing(t *testing.T) {
	t.Parallel()
	names := sweepNamed()
	unused := appAccountOf(names, environment.TierPreview, "shop", "web")
	server := &serviceAccountListing{pages: [][]listedAccount{
		{{id: names.PushAccount(environment.TierPreview), description: "push"}},
		{unused},
	}}
	b, log := sweepingFor(t, server)

	if err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); !slices.Equal(got, []string{unused.id}) {
		t.Errorf("deleted %v, want %s from the second page", got, unused.id)
	}
	if !slices.Equal(server.tokens, []string{"", "page-1"}) {
		t.Errorf("page tokens = %q, want the second request to carry the first answer's token", server.tokens)
	}
}

func TestABootstrapThatCannotDeleteOneAccountStillDeletesTheOthersAndSaysWhichFailed(t *testing.T) {
	t.Parallel()
	names := sweepNamed()
	first := appAccountOf(names, environment.TierPreview, "shop", "web")
	second := appAccountOf(names, environment.TierPreview, "shop", "api")
	server := listing(first, second)
	server.refusing = map[string]int{first.id: http.StatusForbidden}
	b, log := sweepingFor(t, server)

	err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log)
	if err == nil || !strings.Contains(err.Error(), first.id) {
		t.Fatalf("deleteUnusedAccounts() = %v, want an error naming %s", err, first.id)
	}
	if got := server.deletes(); !slices.Equal(got, []string{second.id}) {
		t.Errorf("deleted %v, want %s", got, second.id)
	}
	if !slices.Contains(log.Lines(), deletedOne) {
		t.Errorf("progress = %q, want a count of 1", log.Lines())
	}
}

func TestARepairingDeployDeletesNoServiceAccount(t *testing.T) {
	t.Parallel()
	server := &serviceAccountListing{}
	b, log := sweepingFor(t, server)
	req := sweepRequest(environment.TierPreview)
	req.Repair = true

	if err := b.deleteUnusedAccounts(context.Background(), req, log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if server.listed != 0 || len(server.deletes()) != 0 {
		t.Errorf("a repair made %d list calls and %d deletes, want none: the deploy credential cannot delete accounts", server.listed, len(server.deletes()))
	}
}

func TestAnAppAccountIsReadBackFromTheDescriptionItWasCreatedWith(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	spec := appNamed("web", reachingTopics(routedNextSpec()))
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	if _, err := p.ensureAppAccount(context.Background(), c, spec, nil); err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}
	if len(server.created) == 0 {
		t.Fatal("ensureAppAccount created no account")
	}
	made := server.created[0]

	got, ok := c.readAppAccount(&iam.ServiceAccount{Email: made.AccountId + "@acme-prod" + accountDomain, Description: made.ServiceAccount.Description})
	want := appAccount{id: made.AccountId, tier: spec.Ref.Tier, project: spec.Ref.Project, app: spec.App.App}
	if !ok || got != want {
		t.Errorf("readAppAccount() = %+v, %v, want %+v, true", got, ok, want)
	}
}

const (
	workloadDescriptionOnMain = "the identity every app ocel deploys in the preview tier runs as"
	delayDescriptionOnBranch  = "the identity a delayed message of the preview tier is published to its topic as"
)

func workloadPolicy() *cloudresourcemanager.Policy {
	const member = "serviceAccount:ocel-preview@acme-prod.iam.gserviceaccount.com"
	return &cloudresourcemanager.Policy{Etag: "BwXhoLA=", Bindings: []*cloudresourcemanager.Binding{
		{Role: "roles/datastore.viewer", Members: []string{member, "serviceAccount:other@acme-prod.iam.gserviceaccount.com"}},
		{Role: "roles/datastore.user", Members: []string{member}},
		{Role: "roles/cloudkms.cryptoKeyDecrypter", Members: []string{member}},
		{Role: "roles/viewer", Members: []string{member}},
	}}
}

func TestABootstrapRevokesThenDeletesItsTiersRetiredWorkloadAccount(t *testing.T) {
	t.Parallel()
	names := sweepNamed()
	retired := listedAccount{id: "ocel-preview", description: workloadDescriptionOnMain}
	otherTier := listedAccount{id: "ocel-production", description: "the identity every app ocel deploys in the production tier runs as"}
	running := recordedApp{environment.TierPreview, "shop", stackOf("pr-8", "web", "r1")}

	for name, description := range map[string]string{
		"made by hand":                         "made by hand",
		"described as an unreleased build did": delayDescriptionOnBranch,
	} {
		kept := listing(listedAccount{id: "ocel-preview", description: description}, otherTier)
		kept.policy = workloadPolicy()
		b, log := sweepingFor(t, kept, running)
		if err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log); err != nil {
			t.Fatalf("%s: deleteUnusedAccounts() = %v", name, err)
		}
		if got := kept.deletes(); len(got) != 0 || len(kept.events) != 0 {
			t.Errorf("%s: deleted %v and wrote %v, want nothing", name, got, kept.events)
		}
	}

	retiring := listing(retired, otherTier, appAccountOf(names, environment.TierPreview, "shop", "web"))
	retiring.policy = workloadPolicy()
	b, log := sweepingFor(t, retiring, running)
	if err := b.deleteUnusedAccounts(context.Background(), sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if want := []string{"revoke", "delete ocel-preview"}; !slices.Equal(retiring.events, want) {
		t.Errorf("events = %v, want %v whatever the records hold", retiring.events, want)
	}
	assertWorkloadRevoked(t, retiring.policy)
	if !slices.Contains(log.Lines(), "INFO Deleted the ocel-preview service account: every app of the preview tier runs as an account of its own") {
		t.Errorf("progress = %q, want the retired account said", log.Lines())
	}
}

func assertWorkloadRevoked(t *testing.T, policy *cloudresourcemanager.Policy) {
	t.Helper()
	var held []string
	for _, binding := range policy.Bindings {
		if slices.Contains(binding.Members, "serviceAccount:ocel-preview@acme-prod.iam.gserviceaccount.com") {
			held = append(held, binding.Role)
		}
	}
	if want := []string{"roles/viewer"}; !slices.Equal(held, want) {
		t.Errorf("the retired account still holds %v, want only %v: ocel never granted that one", held, want)
	}
	for _, binding := range policy.Bindings {
		if binding.Role == "roles/datastore.viewer" && !slices.Equal(binding.Members, []string{"serviceAccount:other@acme-prod.iam.gserviceaccount.com"}) {
			t.Errorf("datastore.viewer members = %v, want the other account kept", binding.Members)
		}
	}
}

func TestRemovingATierRevokesThenDeletesItsRetiredWorkloadAccount(t *testing.T) {
	t.Parallel()
	retired := listedAccount{id: "ocel-preview", description: workloadDescriptionOnMain}
	server := &serviceAccountListing{byID: map[string]listedAccount{"ocel-preview": retired}, policy: workloadPolicy()}
	b, log := sweepingFor(t, server)
	if err := b.deleteRetiredWorkloadAccount(context.Background(), environment.TierPreview, log); err != nil {
		t.Fatalf("deleteRetiredWorkloadAccount() = %v", err)
	}
	if want := []string{"revoke", "delete ocel-preview"}; !slices.Equal(server.events, want) {
		t.Errorf("events = %v, want %v", server.events, want)
	}
	assertWorkloadRevoked(t, server.policy)

	for name, byID := range map[string]map[string]listedAccount{
		"one made by hand":             {"ocel-preview": {id: "ocel-preview", description: "made by hand"}},
		"one an unreleased build made": {"ocel-preview": {id: "ocel-preview", description: delayDescriptionOnBranch}},
		"none":                         {},
	} {
		server := &serviceAccountListing{byID: byID, policy: workloadPolicy()}
		b, log := sweepingFor(t, server)
		if err := b.deleteRetiredWorkloadAccount(context.Background(), environment.TierPreview, log); err != nil {
			t.Fatalf("%s: deleteRetiredWorkloadAccount() = %v", name, err)
		}
		if got := server.deletes(); len(got) != 0 || len(server.events) != 0 {
			t.Errorf("%s: deleted %v and wrote %v, want nothing", name, got, server.events)
		}
	}
}

func TestABootstrapChecksItMayListServiceAccounts(t *testing.T) {
	t.Parallel()
	permissions := permissionsFor(nil)
	for _, want := range []string{"iam.serviceAccounts.list", "iam.serviceAccounts.delete"} {
		if !slices.Contains(permissions, want) {
			t.Errorf("permissionsFor(nil) lacks %s", want)
		}
	}
}
