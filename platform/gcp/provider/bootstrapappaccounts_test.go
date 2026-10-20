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
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
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
	return a.id + "@" + project + names.AccountDomain
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
	onDelete func()
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
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, collection+"/"), "@acme-prod"+names.AccountDomain)
			account, found := s.byID[id]
			if !found {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"not found"}}`))
				return
			}
			json.NewEncoder(w).Encode(&iam.ServiceAccount{Email: account.emailOr("acme-prod"), Description: account.description})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, collection+"/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, collection+"/"), "@acme-prod"+names.AccountDomain)
			if code, refused := s.refusing[id]; refused {
				w.WriteHeader(code)
				w.Write([]byte(`{"error":{"code":` + strconv.Itoa(code) + `,"message":"refused"}}`))
				return
			}
			if s.onDelete != nil {
				s.onDelete()
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

func sweepingFor(t *testing.T, s *serviceAccountListing, stacks ...recordedApp) (bootstrap.Engine, *fake.Log) {
	t.Helper()
	return sweepingWith(t, s, &googletest.IAM{}, stacks...)
}

func sweepingWith(t *testing.T, s *serviceAccountListing, server *googletest.IAM, stacks ...recordedApp) (bootstrap.Engine, *fake.Log) {
	t.Helper()
	records := fake.NewKeyValues()
	for _, recorded := range stacks {
		if err := stackrecords.Write(context.Background(), records, recorded.tier, recorded.project, recorded.stack, stackrecords.Stack{}); err != nil {
			t.Fatalf("record %s: %v", recorded.stack, err)
		}
	}
	return composed(bootstrap.Engine{Clients: server.Serve(t, s.handler(t)), Records: records}), &fake.Log{}
}

const sweptEnv = "pr-8"

func appAccountOf(c *gcloud.Clients, tier environment.Tier, project, app string) listedAccount {
	return listedAccount{id: c.AppAccount(project, sweptEnv, app), description: release.AppAccountDescription(tier, project, sweptEnv, app)}
}

func sweepNames() names.Names { return names.New("ocel", "acme-prod") }

func sweepNamed() *gcloud.Clients { return &gcloud.Clients{Names: sweepNames()} }

func sweepRequest(tier environment.Tier) provider.BootstrapRequest {
	return provider.BootstrapRequest{Tier: tier}
}

const deletedOne = "INFO Deleted 1 of the preview tier's app service accounts: no environment runs their apps any more"

func TestABootstrapDeletesTheAccountOfAnAppNoEnvironmentOfItsTierRuns(t *testing.T) {
	t.Parallel()
	unused := appAccountOf(sweepNamed(), environment.TierPreview, "shop", "web")
	server := listing(unused)
	b, log := sweepingFor(t, server)

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); !slices.Equal(got, []string{unused.id}) {
		t.Errorf("deleted %v, want only %s", got, unused.id)
	}
	if !slices.Contains(log.Lines(), deletedOne) {
		t.Errorf("progress = %q, want the count said", log.Lines())
	}
}

func TestABootstrapKeepsTheAccountOfAnAppItsEnvironmentStillRuns(t *testing.T) {
	t.Parallel()
	running := appAccountOf(sweepNamed(), environment.TierPreview, "shop", "web")
	server := listing(running)
	b, log := sweepingFor(t, server, recordedApp{environment.TierPreview, "shop", stackOf("pr-8", "web", "r1")})

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); len(got) != 0 {
		t.Errorf("deleted %v, want nothing: app web still runs in pr-8, the environment its account is for", got)
	}
	if !slices.Contains(log.Lines(), "DEBUG No app service account of the preview tier is unused") {
		t.Errorf("progress = %q, want the nothing-unused line", log.Lines())
	}
}

func TestABootstrapDeletesTheAccountOfAnEnvironmentGoneThoughAnotherEnvironmentRunsTheApp(t *testing.T) {
	t.Parallel()
	gone := appAccountOf(sweepNamed(), environment.TierPreview, "shop", "web")
	server := listing(gone)
	b, log := sweepingFor(t, server, recordedApp{environment.TierPreview, "shop", stackOf("pr-9", "web", "r1")})

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); !slices.Equal(got, []string{gone.id}) {
		t.Errorf("deleted %v, want %s: pr-8 runs nothing, and pr-9 runs web as an account of its own", got, gone.id)
	}
}

func TestABootstrapDeletesTheAccountOfAnAppWhoseProjectOnlyHoldsOtherAppsAndInfrastructure(t *testing.T) {
	t.Parallel()
	unused := appAccountOf(sweepNamed(), environment.TierPreview, "shop", "web")
	server := listing(unused)
	b, log := sweepingFor(t, server,
		recordedApp{environment.TierPreview, "shop", naming.InfraStack("pr-8")},
		recordedApp{environment.TierPreview, "shop", stackOf("pr-8", "api", "r1")})

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); !slices.Equal(got, []string{unused.id}) {
		t.Errorf("deleted %v, want %s: only app api and the infrastructure are recorded", got, unused.id)
	}
}

func TestABootstrapKeepsEveryTierAccountWhateverItsDescriptionSays(t *testing.T) {
	t.Parallel()
	names := sweepNamed()
	held := composed(bootstrap.Engine{Clients: names})
	forged := release.AppAccountDescription(environment.TierPreview, "shop", sweptEnv, "web")
	var accounts []listedAccount
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		for _, id := range []string{names.PushAccount(tier), names.RealtimeAccount(tier), names.EnvSourceSyncAccount(tier)} {
			purpose, err := held.PurposeOf(tier, id)
			if err != nil {
				t.Fatalf("purposeOf(%s, %s) = %v", tier, id, err)
			}
			accounts = append(accounts,
				listedAccount{id: id, description: purpose.Description},
				listedAccount{id: id, description: forged})
		}
	}
	accounts = append(accounts,
		listedAccount{id: names.Connector(), description: "the connector"},
		listedAccount{id: names.Connector(), description: forged})
	server := listing(accounts...)
	b, log := sweepingFor(t, server)

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(environment.TierPreview), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); len(got) != 0 {
		t.Errorf("deleted %v, want no tier account", got)
	}
}

func TestABootstrapKeepsAccountsOfAnotherTierAnotherNamespaceOrAnotherMaker(t *testing.T) {
	t.Parallel()
	named := sweepNamed()
	otherTier := appAccountOf(named, environment.TierProduction, "shop", "web")
	own := appAccountOf(named, environment.TierPreview, "shop", "web")
	otherNamespace := listedAccount{id: "other" + strings.TrimPrefix(own.id, "ocel"), description: own.description}
	forged := listedAccount{id: "ocel-0123456789", description: own.description}
	otherProject := listedAccount{id: own.id, email: own.id + "@elsewhere" + names.AccountDomain, description: own.description}
	server := listing(otherTier, otherNamespace, forged, otherProject)
	b, log := sweepingFor(t, server)

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(environment.TierPreview), log); err != nil {
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

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(environment.TierPreview), log); err != nil {
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

	err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(environment.TierPreview), log)
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

func TestAnAppAccountIsReadBackFromTheDescriptionItWasCreatedWith(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	spec := appNamed("web", reachingTopics(routedNextSpec()))
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	if _, err := p.release().EnsureAccount(context.Background(), c, spec, nil); err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}
	if len(server.Created) == 0 {
		t.Fatal("ensureAppAccount created no account")
	}
	made := server.Created[0]

	got, ok := release.ReadAppAccount(c.Names, &iam.ServiceAccount{Email: made.AccountId + "@acme-prod" + names.AccountDomain, Description: made.ServiceAccount.Description})
	want := release.AppAccount{ID: made.AccountId, Tier: spec.Ref.Tier, Project: spec.Ref.Project, Env: spec.Ref.Name.Env, App: spec.App.App}
	if !ok || got != want {
		t.Errorf("readAppAccount() = %+v, %v, want %+v, true", got, ok, want)
	}
}

func TestABootstrapChecksItMayListServiceAccounts(t *testing.T) {
	t.Parallel()
	permissions := bootstrap.PermissionsFor(nil)
	for _, want := range []string{"iam.serviceAccounts.list", "iam.serviceAccounts.delete"} {
		if !slices.Contains(permissions, want) {
			t.Errorf("permissionsFor(nil) lacks %s", want)
		}
	}
}

type countingRecords struct {
	records.Store
	mu    sync.Mutex
	lists int
	after func(listed int)
}

func (c *countingRecords) List(ctx context.Context, in records.Partition, extra ...string) ([]records.Entry, error) {
	entries, err := c.Store.List(ctx, in, extra...)
	c.mu.Lock()
	c.lists++
	listed := c.lists
	c.mu.Unlock()
	if c.after != nil {
		c.after(listed)
	}
	return entries, err
}

func TestABootstrapReadsAProjectsRecordsOnceToChooseAndOnceMoreBeforeEachDelete(t *testing.T) {
	t.Parallel()
	names := sweepNamed()
	tier := environment.TierPreview
	server := listing(
		appAccountOf(names, tier, "shop", "web"),
		appAccountOf(names, tier, "shop", "api"),
		appAccountOf(names, tier, "shop", "worker"),
		appAccountOf(names, tier, "shop", "cron"),
	)
	b, log := sweepingFor(t, server, recordedApp{tier, "shop", stackOf("pr-8", "web", "r1")}, recordedApp{tier, "shop", stackOf("pr-8", "api", "r1")})
	records := &countingRecords{Store: b.Records}
	b.Records = records

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(tier), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got, want := server.deletes(), []string{names.AppAccount("shop", sweptEnv, "worker"), names.AppAccount("shop", sweptEnv, "cron")}; !slices.Equal(got, want) {
		t.Errorf("deleted %v, want %v", got, want)
	}
	if records.lists != 3 {
		t.Errorf("read the records %d times, want 3: once for the project and once before each of the two deletes", records.lists)
	}
}

func TestABootstrapKeepsTheAccountOfAnAppDeployedWhileItSweeps(t *testing.T) {
	t.Parallel()
	names := sweepNamed()
	tier := environment.TierPreview
	server := listing(appAccountOf(names, tier, "shop", "web"))
	b, log := sweepingFor(t, server)
	records := &countingRecords{Store: b.Records}
	records.after = func(listed int) {
		if listed == 1 {
			if err := stackrecords.Write(context.Background(), b.Records, tier, "shop", stackOf(sweptEnv, "web", "r2"), stackrecords.Stack{}); err != nil {
				t.Errorf("record the concurrent deploy: %v", err)
			}
		}
	}
	b.Records = records

	if err := release.DeleteUnusedAccounts(context.Background(), b, sweepRequest(tier), log); err != nil {
		t.Fatalf("deleteUnusedAccounts() = %v", err)
	}
	if got := server.deletes(); len(got) != 0 {
		t.Errorf("deleted %v, want nothing: a deploy recorded the app after the sweep first read the records", got)
	}
}
