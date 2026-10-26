package vps_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func cloudflareCounting(t *testing.T) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUDFLARE_BASE_URL", srv.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	t.Setenv(provider.NamespaceEnvVar, "ocel")
	return &calls
}

func TestADeployToATierWhoseFrontingStoreIsRecordedCallsNoCloudflareAPI(t *testing.T) {
	calls := cloudflareCounting(t)
	p := over(&box{})
	p.Recording(fake.NewKeyValues())
	p.Sealing(fake.NewCipher())
	if err := p.RecordFronting(t.Context(), environment.TierProduction, "box-credential", "seed", "ocel-releases-store", "https://store.example"); err != nil {
		t.Fatalf("RecordFronting: %v", err)
	}

	program, err := p.ProgramEdge(t.Context(), provider.EdgeProgramRequest{Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("ProgramEdge: %v", err)
	}

	if n := calls.Load(); n != 0 {
		t.Errorf("ProgramEdge made %d Cloudflare API calls, want none once the tier's store is recorded", n)
	}
	if program.Origin.Images == nil || program.Origin.Images.MaxRequestBytes <= 0 {
		t.Errorf("ProgramEdge names images %+v, want a request limit: the worker posts a Next app's images to the box", program.Origin.Images)
	}
	if store := program.ReleasesStore; store.Name != "ocel-releases-store" || store.Endpoint != "https://store.example" || store.BootstrapCredential != "box-credential" {
		t.Errorf("ProgramEdge hands the edge store %+v, want the recorded name, endpoint and credential", store)
	}
}

type storeInstalls struct {
	mu          sync.Mutex
	credentials []string
}

func (s *storeInstalls) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.credentials)
}

func installingStores(p *vps.Provider, endpoint string) *storeInstalls {
	installs := &storeInstalls{}
	p.InstallingStores(func(_ context.Context, _ environment.Tier, credential string) (edge.ReleasesStore, error) {
		installs.mu.Lock()
		defer installs.mu.Unlock()
		installs.credentials = append(installs.credentials, credential)
		return edge.ReleasesStore{Name: "ocel-releases-store", Endpoint: endpoint, BootstrapCredential: credential}, nil
	})
	return installs
}

func TestADeployToATierWhoseRecordedStoreIsCurrentInstallsNothing(t *testing.T) {
	calls := cloudflareCounting(t)
	p := frontingOver(t, "https://store.example")
	installs := installingStores(p, "https://store.example")

	if _, err := p.ProgramEdge(t.Context(), provider.EdgeProgramRequest{Tier: environment.TierProduction}); err != nil {
		t.Fatalf("ProgramEdge: %v", err)
	}

	if n := installs.count(); n != 0 {
		t.Errorf("installed the store %d times, want none while the record says it is this build's", n)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("made %d Cloudflare API calls, want none", n)
	}
}

func TestADeployToATierWhoseRecordedStoreIsBehindThisBuildInstallsItOnceAndRecordsIt(t *testing.T) {
	cloudflareCounting(t)
	p := frontingOver(t, "https://store.example")
	if err := p.RecordFrontingStoreVersion(t.Context(), environment.TierProduction, "an earlier build's"); err != nil {
		t.Fatalf("RecordFrontingStoreVersion: %v", err)
	}
	installs := installingStores(p, "https://store.example")

	for range 2 {
		if _, err := p.ProgramEdge(t.Context(), provider.EdgeProgramRequest{Tier: environment.TierProduction}); err != nil {
			t.Fatalf("ProgramEdge: %v", err)
		}
	}

	if installs.count() != 1 || installs.credentials[0] != "box-credential" {
		t.Errorf("installed the store with %v, want once with the recorded credential", installs.credentials)
	}
	if version, err := p.ReadFrontingStoreVersion(t.Context(), environment.TierProduction); err != nil || version != cloudflare.ComputeStoreVersion() {
		t.Errorf("the record names store version %q (%v), want this build's", version, err)
	}
}

func TestAStoreFoundBehindDespiteItsRecordIsInstalledByTheNextDeploy(t *testing.T) {
	cloudflareCounting(t)
	p := frontingOver(t, "https://store.example")
	installs := installingStores(p, "https://store.example")

	if err := vps.ForgetFrontingStoreVersion(p, t.Context(), environment.TierProduction); err != nil {
		t.Fatalf("ForgetFrontingStoreVersion: %v", err)
	}
	if _, err := p.ProgramEdge(t.Context(), provider.EdgeProgramRequest{Tier: environment.TierProduction}); err != nil {
		t.Fatalf("ProgramEdge: %v", err)
	}

	if n := installs.count(); n != 1 {
		t.Errorf("installed the store %d times, want once", n)
	}
}

type removal struct {
	path   string
	bearer string
	body   string
}

func keptRemovals(t *testing.T) (*httptest.Server, *[]removal) {
	t.Helper()
	var mu sync.Mutex
	var seen []removal
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, removal{path: r.URL.Path, bearer: r.Header.Get("Authorization"), body: string(body)})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func frontingOver(t *testing.T, endpoint string, projects ...string) *vps.Provider {
	t.Helper()
	p := over(&box{})
	p.Recording(fake.NewKeyValues())
	p.Sealing(fake.NewCipher())
	if err := p.RecordFronting(t.Context(), environment.TierProduction, "box-credential", "seed", "ocel-releases-store", endpoint, projects...); err != nil {
		t.Fatalf("RecordFronting: %v", err)
	}
	return p
}

func TestAProjectTheWorkerStopsFrontingHasWhatTheEdgeKeptOfItRemovedAndLeavesTheOthersKept(t *testing.T) {
	calls := cloudflareCounting(t)
	store, removed := keptRemovals(t)
	p := frontingOver(t, store.URL, "shop", "blog")

	if err := vps.ForgetFrontedProject(p, t.Context(), environment.TierProduction, "shop"); err != nil {
		t.Fatalf("ForgetFrontedProject: %v", err)
	}

	if len(*removed) != 1 || (*removed)[0].path != "/shop/objects/remove" || (*removed)[0].bearer != "Bearer box-credential" {
		t.Fatalf("the store was sent %+v, want one removal of shop's objects", *removed)
	}
	for _, prefix := range []string{"assets/shop/", "image-config/shop/", "route-tables/shop/", "bundles/shop/"} {
		if !strings.Contains((*removed)[0].body, `"`+prefix+`"`) {
			t.Errorf("the removal names %s, want %s among its prefixes", (*removed)[0].body, prefix)
		}
	}
	if projects, err := p.ReadFrontedProjects(t.Context(), environment.TierProduction); err != nil || !slices.Equal(projects, []string{"blog"}) {
		t.Errorf("the record names projects %v (%v), want blog alone", projects, err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("made %d Cloudflare API calls, want none while another project keeps the store", n)
	}
}

func TestAProjectTheWorkerNeverFrontedRemovesNothing(t *testing.T) {
	store, removed := keptRemovals(t)
	p := frontingOver(t, store.URL, "blog")

	if err := vps.ForgetFrontedProject(p, t.Context(), environment.TierProduction, "shop"); err != nil {
		t.Fatalf("ForgetFrontedProject: %v", err)
	}

	if len(*removed) != 0 {
		t.Errorf("the store was sent %+v, want nothing", *removed)
	}
}

type storeTeardowns struct {
	mu     sync.Mutex
	during func()
	tiers  []environment.Tier
}

func (s *storeTeardowns) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tiers)
}

func tearingDownStores(p *vps.Provider) *storeTeardowns {
	teardowns := &storeTeardowns{}
	p.TearingDownStores(func(_ context.Context, tier environment.Tier) error {
		teardowns.mu.Lock()
		teardowns.tiers = append(teardowns.tiers, tier)
		during := teardowns.during
		teardowns.mu.Unlock()
		if during != nil {
			during()
		}
		return nil
	})
	return teardowns
}

func projectsSharingATier(t *testing.T, endpoint string, recorded ...string) (*fake.KeyValues, *vps.Provider, *vps.Provider) {
	t.Helper()
	keyValues, cipher := fake.NewKeyValues(), fake.NewCipher()
	shop, blog := over(&box{}), over(&box{})
	for project, p := range map[string]*vps.Provider{"shop": shop, "blog": blog} {
		p.Recording(keyValues)
		p.Sealing(cipher)
		p.Deploying(project)
	}
	if err := shop.RecordFronting(t.Context(), environment.TierProduction, "box-credential", "seed", "ocel-releases-store", endpoint, recorded...); err != nil {
		t.Fatalf("RecordFronting: %v", err)
	}
	return keyValues, shop, blog
}

func TestTheLastFrontedProjectLeavingTearsNothingDownWhenAnotherProjectIsFrontedBeforeItsDecision(t *testing.T) {
	cloudflareCounting(t)
	store, _ := keptRemovals(t)
	keyValues, shop, blog := projectsSharingATier(t, store.URL, "shop")
	teardowns := tearingDownStores(shop)
	keyValues.BeforeNextWrite(vps.FrontingRecordKey(environment.TierProduction), func() {
		if _, err := blog.ProgramEdge(t.Context(), provider.EdgeProgramRequest{Tier: environment.TierProduction}); err != nil {
			t.Errorf("blog's ProgramEdge: %v", err)
		}
	})

	if err := vps.ForgetFrontedProject(shop, t.Context(), environment.TierProduction, "shop"); err != nil {
		t.Fatalf("ForgetFrontedProject: %v", err)
	}

	if n := teardowns.count(); n != 0 {
		t.Errorf("tore the tier's store down %d times, want none while blog is fronted", n)
	}
	if projects, err := shop.ReadFrontedProjects(t.Context(), environment.TierProduction); err != nil || !slices.Equal(projects, []string{"blog"}) {
		t.Errorf("the record names projects %v (%v), want blog alone", projects, err)
	}
}

func TestAProjectFrontedWhileTheTiersStoreIsTornDownIsRefusedAndInstallsItOnceTheTeardownEnds(t *testing.T) {
	cloudflareCounting(t)
	store, _ := keptRemovals(t)
	_, shop, blog := projectsSharingATier(t, store.URL, "shop")
	teardowns := tearingDownStores(shop)
	installs := installingStores(blog, store.URL)
	var during error
	teardowns.during = func() {
		_, during = blog.ProgramEdge(t.Context(), provider.EdgeProgramRequest{Tier: environment.TierProduction})
	}

	if err := vps.ForgetFrontedProject(shop, t.Context(), environment.TierProduction, "shop"); err != nil {
		t.Fatalf("ForgetFrontedProject: %v", err)
	}
	var refused refusal.Refusal
	if !errors.As(during, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("blog's ProgramEdge during the teardown = %v, want a not-ready refusal", during)
	}
	if recorded, err := shop.IsFrontingRecorded(t.Context(), environment.TierProduction); err != nil || recorded {
		t.Fatalf("the fronting record is still held (%v) after the teardown, want it gone", err)
	}

	if _, err := blog.ProgramEdge(t.Context(), provider.EdgeProgramRequest{Tier: environment.TierProduction}); err != nil {
		t.Fatalf("blog's ProgramEdge after the teardown: %v", err)
	}

	if n := installs.count(); n != 1 {
		t.Errorf("installed the store %d times, want once, after the teardown", n)
	}
	if projects, err := blog.ReadFrontedProjects(t.Context(), environment.TierProduction); err != nil || !slices.Equal(projects, []string{"blog"}) {
		t.Errorf("the record names projects %v (%v), want blog alone", projects, err)
	}
}

func TestRemovingTheLastProjectFrontedOnATierRemovesWhatTheEdgeKeptOfItAndTheTiersStore(t *testing.T) {
	cloudflareCounting(t)
	store, removed := keptRemovals(t)
	p := frontingOver(t, store.URL, "shop")
	teardowns := tearingDownStores(p)
	removal := p.Hooks().ProjectRemoval
	if removal == nil {
		t.Fatal("the box removes nothing of a project outside its stacks, want what the edge keeps of it removed")
	}

	groups, err := removal.Plan(t.Context(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Changes) != 2 {
		t.Errorf("the removal plan is %+v, want one group naming shop's kept files and the tier's store", groups)
	}
	if err := removal.Remove(t.Context(), environment.TierProduction, "shop", progress.Discard()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if len(*removed) != 1 || (*removed)[0].path != "/shop/objects/remove" {
		t.Errorf("the store was sent %+v, want one removal of shop's objects", *removed)
	}
	if n := teardowns.count(); n != 1 {
		t.Errorf("tore the tier's store down %d times, want once: no project on it is fronted", n)
	}
	if recorded, err := p.IsFrontingRecorded(t.Context(), environment.TierProduction); err != nil || recorded {
		t.Errorf("the fronting record is still held (%v), want it gone", err)
	}
}

func TestRemovingAProjectTheEdgeNeverFrontedPlansAndRemovesNothingAtCloudflare(t *testing.T) {
	calls := cloudflareCounting(t)
	store, removed := keptRemovals(t)
	p := frontingOver(t, store.URL, "blog")
	removal := p.Hooks().ProjectRemoval

	groups, err := removal.Plan(t.Context(), environment.TierProduction, "shop")
	if err != nil || len(groups) != 0 {
		t.Errorf("Plan = %+v (%v), want nothing", groups, err)
	}
	if err := removal.Remove(t.Context(), environment.TierProduction, "shop", progress.Discard()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if len(*removed) != 0 || calls.Load() != 0 {
		t.Errorf("sent the store %+v and made %d Cloudflare calls, want none", *removed, calls.Load())
	}
}

func TestATeardownLeftUnfinishedPastItsLeaseIsTakenOverByTheNextFrontedProject(t *testing.T) {
	cloudflareCounting(t)
	_, shop, blog := projectsSharingATier(t, "https://store.example")
	if err := shop.RecordFrontingRetiring(t.Context(), environment.TierProduction, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("RecordFrontingRetiring: %v", err)
	}
	installs := installingStores(blog, "https://store.example")

	if _, err := blog.ProgramEdge(t.Context(), provider.EdgeProgramRequest{Tier: environment.TierProduction}); err != nil {
		t.Fatalf("ProgramEdge: %v", err)
	}

	if n := installs.count(); n != 1 {
		t.Errorf("installed the store %d times, want once", n)
	}
	if projects, err := blog.ReadFrontedProjects(t.Context(), environment.TierProduction); err != nil || !slices.Equal(projects, []string{"blog"}) {
		t.Errorf("the record names projects %v (%v), want blog alone", projects, err)
	}
}
