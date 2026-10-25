package providerserver_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestRemoveStalePromotionsKeepsTheNewestN(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2", "p3")

	stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug:        "shop",
		KeepN:       2,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("RemoveStalePromotions() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.GetPromotions()) != 2 {
		t.Errorf("after the sweep the history has %d promotion(s), want the 2 kept", len(listed.GetPromotions()))
	}
}

func TestAPruneSaysWhichPromotionsItReclaimedAndHowManyItKept(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		keep  int32
		seed  bool
		wants string
	}{
		{"past the newest two", 2, true, "Reclaimed promotion p1, kept 2"},
		{"when every promotion is within the newest", 5, true, "Nothing to prune: all 3 promotions are kept"},
		{"when nothing was ever deployed", 2, false, "Nothing to prune: shop has no deploy in prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, provider := contractServed(t, "1.0.0")
			if tc.seed {
				edgeProvisioned(t, provider, environment.TierProduction, "shop")
				seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2", "p3")
			}

			stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
				Slug:        "shop",
				KeepN:       tc.keep,
				Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
			})
			if err != nil {
				t.Fatalf("RemoveStalePromotions() error = %v", err)
			}
			var said []string
			for _, event := range recorded(stream) {
				if line := saidLine(event); line != "" {
					said = append(said, line)
				}
			}
			if !slices.Contains(said, tc.wants) || (tc.name == "past the newest two" && !slices.Contains(said, "Destroying the stack of web release 00000000000000000000000000000001~000000000001 (1 of 1)")) {
				t.Errorf("the prune said %q, want %q", said, tc.wants)
			}
		})
	}
}

type serviceEveryReleaseRevises struct {
	mu               sync.Mutex
	revisions        int
	removed          []string
	removedRevisions []string
}

func (s *serviceEveryReleaseRevises) hooks() resources.Hooks {
	return resources.Hooks{Functions: &resources.FunctionHooks{
		Provision: func(context.Context, provider.StackSpec, progress.Log) ([]provider.Function, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.revisions++
			return []provider.Function{{Name: "api", Physical: "shop-web-api", Revision: fmt.Sprintf("shop-web-api-%05d", s.revisions)}}, nil
		},
		Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ provider.ImageStore, _ progress.Log) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, function := range functions {
				s.removed = append(s.removed, function.Physical)
			}
			return nil
		},
		Shared: &resources.SharedHooks[provider.Function]{
			Name: func(context.Context, provider.StackSpec) ([]provider.Function, error) {
				return []provider.Function{{Name: "api", Physical: "shop-web-api"}}, nil
			},
			RemoveRevisions: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ provider.ImageStore, _ progress.Log) ([]provider.Function, error) {
				s.mu.Lock()
				defer s.mu.Unlock()
				for _, function := range functions {
					s.removedRevisions = append(s.removedRevisions, function.Revision)
				}
				return nil, nil
			},
		},
	}}
}

func TestAPruneTakesOnlyTheDroppedReleasesRevisionFromTheServiceTheKeptReleasesServeFrom(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{}
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).ResourceStacks(service.hooks())
	client := servedProvider(t, "1.0.0", vendor)
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1", "p2", "p3")
	for i := range 3 {
		ref := provider.StackRef{
			Project: "shop",
			Tier:    environment.TierProduction,
			Name:    naming.AppStack(stackrecords.ProductionEnv, "web", releaseOf(t, releaseFor(i))),
		}
		result, err := vendor.Stacks().Provision(context.Background(), provider.StackSpec{
			Ref:  ref,
			Kind: provider.StackApp,
			App:  &provider.AppSpec{App: "web", Compute: provider.ComputeServerless, Functions: []provider.FunctionSpec{{Name: "api"}}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := stackrecords.Write(context.Background(), vendor.KeyValues(), ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
			Kind:      provider.StackApp,
			App:       "web",
			Release:   releaseFor(i),
			Functions: result.Functions,
		}); err != nil {
			t.Fatal(err)
		}
	}

	stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug:        "shop",
		KeepN:       2,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("RemoveStalePromotions() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}

	if len(service.removed) != 0 {
		t.Errorf("the prune took down %v, which the kept promotions serve from", service.removed)
	}
	if want := []string{"shop-web-api-00001"}; !slices.Equal(service.removedRevisions, want) {
		t.Errorf("the prune removed revisions %v, want %v: only the revision the pruned promotion's build deployed", service.removedRevisions, want)
	}
}

func TestARollbackPastTheRetainedPromotionsReclaimsTheBuildItDropped(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	releases := seedKept(t, provider)

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if _, found, err := releases.Record(context.Background(), "web", releaseFor(0)); err != nil || found {
		t.Errorf("the record of p00's build = found %v, %v, want it removed once no promotion names it", found, err)
	}
	var destroyed []string
	for _, entry := range provider.Journal() {
		if strings.HasPrefix(entry, "destroy ") {
			destroyed = append(destroyed, entry)
		}
	}
	if len(destroyed) != 1 {
		t.Errorf("the rollback destroyed %v, want the one stack of the build it dropped", destroyed)
	}
}

func TestADeployPastTheRetainedPromotionsReclaimsTheBuildItDropped(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	releases := seedKept(t, provider)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if _, found, err := releases.Record(context.Background(), "web", releaseFor(0)); err != nil || found {
		t.Errorf("the record of p00's build = found %v, %v, want it removed once no promotion names it", found, err)
	}
	want := fmt.Sprintf("Destroying the stack of web release %s (1 of 1)", releaseFor(0))
	var said []string
	for _, event := range events {
		if line := saidLine(event); line != "" {
			said = append(said, line)
		}
	}
	if !slices.Contains(said, want) {
		t.Errorf("the deploy said %q, want %q", said, want)
	}
}

func seedKept(t *testing.T, vendor *fake.Provider) *ledger.Ledger {
	t.Helper()
	ids := make([]string, ledger.KeptPromotions)
	for i := range ids {
		ids[i] = fmt.Sprintf("p%02d", i)
	}
	return seedPromotions(t, vendor, environment.TierProduction, "shop", "", ids...)
}

func warnings(events []*progressv1.OperationEvent) []string {
	var warned []string
	for _, event := range events {
		if event.GetBody() == nil && event.GetLevel() == progressv1.Level_LEVEL_WARN {
			warned = append(warned, event.GetMessage())
		}
	}
	return warned
}

func TestADeployWhoseDroppedBuildCannotBeReclaimedServesAndWarnsWithTheReason(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedKept(t, vendor)
	vendor.FakeStacks().RefuseNextDestroy(errors.New("the stack is locked by another run"))

	result, events := deploy(t, client, deployRequest())

	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() whose reclaim failed = %q, want it to succeed: its promotion serves", result.GetError())
	}
	warned := strings.Join(warnings(events), "\n")
	for _, want := range []string{result.GetPromotionId()[:8], "the stack is locked by another run"} {
		if !strings.Contains(warned, want) {
			t.Errorf("the deploy warned %q, want it to name %q", warned, want)
		}
	}
	if _, found, err := releases.Record(context.Background(), "web", releaseFor(0)); err != nil || !found {
		t.Errorf("the record of p00's build = found %v, %v, want it kept: its stack was not destroyed", found, err)
	}
}

func TestADeployWhoseDroppedRecordCannotBeRemovedStillServesItsPromotion(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedKept(t, vendor)
	record := ledger.Partition(environment.TierProduction, "shop").Key("records", "web", releaseFor(0))
	vendor.KeyValues().(*fake.KeyValues).SetRemovalError(record, errors.New("the table refused the delete"))

	result, events := deploy(t, client, deployRequest())

	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() whose dropped record could not be removed = %q, want it to succeed", result.GetError())
	}
	active, found, err := releases.ReadActive(context.Background(), "")
	if err != nil || !found || active.PromotionID != result.GetPromotionId() {
		t.Fatalf("the ledger names %+v, %v, %v, want %s", active, found, err, result.GetPromotionId())
	}
	if served := relayPlane(vendor).Releases("shop", environment.TierProduction, router.DefaultPointer)["web"]; served != active.Releases["web"] {
		t.Errorf("the router serves web %q, want %q, the build the ledger names", served, active.Releases["web"])
	}
	if !strings.Contains(strings.Join(warnings(events), "\n"), "the table refused the delete") {
		t.Errorf("the deploy warned %q, want the removal that failed named", warnings(events))
	}
}

func TestADeployReclaimsTheBuildItsPromoteDroppedUnderItsEnvironmentLease(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	seedKept(t, vendor)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	destroyed, leased := destroyedStacks(vendor), vendor.FakeStacks().ListDestroysUnderLease()
	if len(destroyed) == 0 {
		t.Fatal("the deploy destroyed nothing, so the lease it destroyed under proves nothing")
	}
	if !slices.Equal(leased, destroyed) {
		t.Errorf("the deploy destroyed %v under its lease, want every stack it destroyed, %v: losing the lease must stop a reclaim", leased, destroyed)
	}
}

func TestADeployRetiresWhatItNoLongerDeploysUnderItsEnvironmentLease(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	var mu sync.Mutex
	var leased []bool
	vendor.WithHooks(func(hooks *provider.Hooks) {
		retire := hooks.Retirement.Retire
		hooks.Retirement = &provider.RetirementHooks{Plan: hooks.Retirement.Plan, Retire: func(ctx context.Context, live provider.LiveApps, progress progress.Log) error {
			mu.Lock()
			leased = append(leased, live.Lease.Covers(provider.StackRef{Project: live.Slug, Tier: live.Tier, Name: naming.StackName{Env: live.Env}}))
			mu.Unlock()
			return retire(ctx, live, progress)
		}}
	})
	client := servedProvider(t, "1.0.0", vendor)
	bootstrappedOverRPC(t, client)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(leased, []bool{true}) {
		t.Errorf("the deploy retired under its lease %v, want once and leased: losing the lease must stop a retire", leased)
	}
}

func TestADeployARouterLeftUnservedStillReclaimsTheBuildItsPromoteDropped(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedKept(t, vendor)
	relayPlane(vendor).FailNextPointerMove(errors.New("the data plane refused the write"))

	result, _ := deploy(t, client, deployRequest())

	if result.GetSuccess() {
		t.Fatal("Deploy() with a router that refused its pointer move = success, want it to fail")
	}
	if _, found, err := releases.Record(context.Background(), "web", releaseFor(0)); err != nil || found {
		t.Errorf("the record of p00's build = found %v, %v, want it reclaimed: the promote dropped p00 whether or not it served", found, err)
	}
	if !slices.ContainsFunc(vendor.Journal(), func(entry string) bool { return strings.HasPrefix(entry, "destroy ") }) {
		t.Errorf("the journal reads %v, want the stack of p00's build destroyed", vendor.Journal())
	}
}

func TestARollbackReclaimsTheBuildItDroppedUnderItsEnvironmentLease(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedKept(t, vendor)

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() = %v", err)
	}

	destroyed, leased := destroyedStacks(vendor), vendor.FakeStacks().ListDestroysUnderLease()
	if len(destroyed) == 0 {
		t.Fatal("the rollback destroyed nothing, so the lease it destroyed under proves nothing")
	}
	if !slices.Equal(leased, destroyed) {
		t.Errorf("the rollback destroyed %v under its lease, want every stack it destroyed, %v", leased, destroyed)
	}
}

func TestARollbackWhoseDroppedBuildCannotBeReclaimedServesAndWarns(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedKept(t, vendor)
	vendor.FakeStacks().RefuseNextDestroy(errors.New("the stack is locked by another run"))

	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() whose reclaim failed = %v, want it to succeed: its promotion serves", err)
	}
	if warned := strings.Join(rolled.warnings(), "\n"); !strings.Contains(warned, "the stack is locked by another run") {
		t.Errorf("Rollback() warned %q, want the destroy that failed named", warned)
	}
}

func TestARollbackReclaimsTheDroppedBuildsBesideARecordNamingNoBuildAndWarnsOfIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	releases := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop")
	ctx := context.Background()
	replaces := ""
	for i := range ledger.KeptPromotions {
		builds := map[string]string{"web": releaseFor(i)}
		if i == 0 {
			builds["api"] = "garbage"
		}
		for app, build := range builds {
			if err := releases.PutStaged(ctx, router.ReleaseRecord{App: app, Release: build}); err != nil {
				t.Fatal(err)
			}
		}
		promotion := router.Promotion{PromotionID: fmt.Sprintf("p%02d", i), Ts: int64(i + 1), Releases: builds}
		if _, err := releases.Promote(ctx, promotion, "", replaces); err != nil {
			t.Fatal(err)
		}
		replaces = promotion.PromotionID
	}

	rolled, err := rollBack(ctx, client, &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if _, found, err := releases.Record(ctx, "web", releaseFor(0)); err != nil || found {
		t.Errorf("the record of p00's web build = found %v, %v, want it reclaimed beside the record it could not read", found, err)
	}
	if _, found, err := releases.Record(ctx, "api", "garbage"); err != nil || !found {
		t.Errorf("the record naming no build = found %v, %v, want it kept: nothing names the stack it would free", found, err)
	}
	if warned := strings.Join(rolled.warnings(), "\n"); !strings.Contains(warned, "garbage") {
		t.Errorf("Rollback() warned %q, want the record it could not reclaim named", warned)
	}
}

func seedKeptContainers(t *testing.T, vendor *fake.Provider) *ledger.Ledger {
	t.Helper()
	releases := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop")
	ctx := context.Background()
	replaces := ""
	for i := range ledger.KeptPromotions {
		record := router.ReleaseRecord{App: "web", Release: releaseFor(i), Image: containerTestImage, Physical: fmt.Sprintf("web-%02d", i)}
		if err := releases.PutStaged(ctx, record); err != nil {
			t.Fatal(err)
		}
		promotion := router.Promotion{PromotionID: fmt.Sprintf("p%02d", i), Ts: int64(i + 1), Releases: map[string]string{"web": record.Release}}
		if _, err := releases.Promote(ctx, promotion, "", replaces); err != nil {
			t.Fatal(err)
		}
		replaces = promotion.PromotionID
	}
	return releases
}

func destroyedStacks(vendor *fake.Provider) []string {
	var destroyed []string
	for _, entry := range vendor.Journal() {
		if stack, found := strings.CutPrefix(entry, "destroy "); found {
			destroyed = append(destroyed, stack)
		}
	}
	return destroyed
}

func TestARollbackPastTheRetainedPromotionsReclaimsTheContainerBuildItDropped(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	releases := seedKeptContainers(t, vendor)
	ctx := context.Background()

	if _, err := rollBack(ctx, client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	want := naming.AppStack(stackrecords.ProductionEnv, "web", releaseOf(t, releaseFor(0))).String()
	if destroyed := destroyedStacks(vendor); !slices.Contains(destroyed, want) {
		t.Errorf("the rollback destroyed %v, want %s: a container release this provider keeps no window of is reclaimed like any other", destroyed, want)
	}
	if _, found, err := releases.Record(ctx, "web", releaseFor(0)); err != nil || found {
		t.Errorf("the record of p00's container build = found %v, %v, want it removed once its stack is", found, err)
	}
}

func TestARollbackPastTheRetainedPromotionsLeavesTheContainerBuildItDroppedToAProviderThatRetainsThem(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	vendor.WithFacts(func(facts *provider.Facts) { facts.RetainsContainerReleases = true })
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	releases := seedKeptContainers(t, vendor)
	ctx := context.Background()

	if _, err := rollBack(ctx, client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if destroyed := destroyedStacks(vendor); len(destroyed) != 0 {
		t.Errorf("the rollback destroyed %v, want the container build it dropped left to the provider that keeps its own window of them", destroyed)
	}
	if _, found, err := releases.Record(ctx, "web", releaseFor(0)); err != nil || found {
		t.Errorf("the record of p00's container build = found %v, %v, want it removed once no promotion names it", found, err)
	}
}

func TestRollingBackToAnEarlierDeployOfTheSameImageServesTheOriginThatDeployProvisioned(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	releases := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop")
	ctx := context.Background()

	served := func() (string, string) {
		t.Helper()
		active, found, err := releases.ReadActive(ctx, "")
		if err != nil || !found {
			t.Fatalf("ReadActive() = %v, %v, want a promotion", found, err)
		}
		record, staged, err := releases.Record(ctx, "web", active.Releases["web"])
		if err != nil || !staged {
			t.Fatalf("the record of web build %s = %v, %v, want it staged", active.Releases["web"], staged, err)
		}
		return active.Releases["web"], record.Origin
	}
	var builds, origins []string
	for version := range int64(2) {
		req := registryDeployRequest()
		req.Manifest.Apps[0].Variables = []*contractv1.ManifestVariable{{
			Key: "GREETING", Value: "hello", Version: version + 1, Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		}}
		result, _ := deploy(t, client, req)
		if result == nil || !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
		}
		build, origin := served()
		builds, origins = append(builds, build), append(origins, origin)
	}
	if builds[0] == builds[1] {
		t.Fatalf("both deploys of one image promoted build %s, want each deploy's record under its own build", builds[0])
	}
	if origins[0] == origins[1] {
		t.Fatalf("both deploys provisioned origin %s, so nothing tells their records apart", origins[0])
	}

	if _, err := rollBack(ctx, client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if build, origin := served(); build != builds[0] || origin != origins[0] {
		t.Errorf("after the rollback web serves build %s at %s, want build %s at %s, the origin the first deploy provisioned", build, origin, builds[0], origins[0])
	}
}

func TestARollbackDestroysTheBuildItDroppedWithTheStoreOfTheRegistryTheProjectNames(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedKeptContainers(t, vendor)

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop", ProjectRegistry: projectRegistry}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	stores := vendor.FakeStacks().DestroyedWith()
	if len(stores) == 0 {
		t.Fatal("Rollback() destroyed no stack")
	}
	for _, store := range stores {
		if store != provider.ImageStore(vendor.ImageStore()) {
			t.Errorf("a dropped build was destroyed with %v, want the store opened on the project's registry: the image it pushed there goes with it", store)
		}
	}
}

func TestAPruneDestroysTheBuildsItDropsWithTheStoreOfTheRegistryTheProjectNames(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1", "p2", "p3")

	stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug:            "shop",
		KeepN:           2,
		Environment:     &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		ProjectRegistry: projectRegistry,
	})
	if err != nil {
		t.Fatalf("RemoveStalePromotions() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}

	stores := vendor.FakeStacks().DestroyedWith()
	if len(stores) == 0 {
		t.Fatal("the prune destroyed no stack")
	}
	for _, store := range stores {
		if store != provider.ImageStore(vendor.ImageStore()) {
			t.Errorf("a pruned build was destroyed with %v, want the store opened on the project's registry: the image it pushed there goes with it", store)
		}
	}
}

type registryRefusingStacks struct {
	provider.Stacks
}

func (s registryRefusingStacks) Destroy(ctx context.Context, req provider.StackDestroy, progress progress.Log) error {
	ref := req.Ref
	progress.Warn("Left " + ref.Name.String() + "'s image in the registry it was pushed to: UNSUPPORTED")
	return s.Stacks.Destroy(ctx, req, progress)
}

func TestARollbackWarnsOfTheImagesItsReclaimLeftInTheProjectsRegistry(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedProvider(t, "1.0.0", refusingStacks{Provider: vendor, stacks: registryRefusingStacks{Stacks: vendor.Stacks()}})
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedKeptContainers(t, vendor)

	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop", ProjectRegistry: projectRegistry})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if !slices.ContainsFunc(rolled.warnings(), func(warning string) bool { return strings.Contains(warning, "UNSUPPORTED") }) {
		t.Errorf("the rollback warned %q, want what its reclaim left in the project's registry", rolled.warnings())
	}
}

func TestARollbackKeepsTheAppsADeployRoutedBeforeItTookProduction(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")
	var routed error
	provider.KeyValues().(*fake.KeyValues).BeforeNextWrite(stackrecords.EnvironmentLeaseKey(environment.TierProduction, "shop", stackrecords.ProductionEnv), func() {
		routed = routeAnotherApp(provider.KeyValues(), environment.TierProduction, "docs")
	})

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if routed != nil {
		t.Fatal(routed)
	}
	if _, kept := readRoutedApps(t, provider.KeyValues(), environment.TierProduction)["docs"]; !kept {
		t.Error("the rollback dropped docs, which a deploy routed before the rollback took production: it wrote back what it read before it held the lease")
	}
}
