package providerserver_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

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

func seedPromotions(t *testing.T, provider *fake.Provider, tier environment.Tier, slug, pointer string, ids ...string) *ledger.Ledger {
	t.Helper()
	releases := ledger.New(provider.KeyValues(), tier, slug)
	replaces := ""
	for i, id := range ids {
		if err := releases.PutStaged(context.Background(), router.DeploymentRecord{App: "web", Build: buildIdentity(i)}); err != nil {
			t.Fatal(err)
		}
		promotion := router.Promotion{PromotionID: id, Ts: int64(i + 1), Builds: map[string]string{"web": buildIdentity(i)}}
		if _, err := releases.Promote(context.Background(), promotion, pointer, replaces); err != nil {
			t.Fatal(err)
		}
		replaces = id
	}
	return releases
}

func buildIdentity(seq int) string {
	return fmt.Sprintf("%032x~%012x", seq+1, seq+1)
}

func TestListPromotionsReadsTheProjectsLedger(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListPromotions() error = %v", err)
	}
	if len(listed.GetPromotions()) != 2 {
		t.Fatalf("ListPromotions() = %v, want both promotions", listed.GetPromotions())
	}
	if !listed.GetPromotions()[0].GetActive() || listed.GetPromotions()[0].GetPromotion().GetPromotionId() != "p2" {
		t.Errorf("ListPromotions() heads with %+v, want p2 active", listed.GetPromotions()[0])
	}
}

func TestListPromotionsIsEmptyForAProjectThatHasNeverDeployed(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListPromotions() error = %v", err)
	}
	if len(listed.GetPromotions()) != 0 {
		t.Errorf("ListPromotions() = %v, want nothing", listed.GetPromotions())
	}
}

func TestRollbackPromotesTheBuildsOfTheEarlierPromotionAsANewOne(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")

	rolled, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	promoted := rolled.GetPromoted()
	if id := promoted.GetPromotionId(); id == "p1" || id == "p2" {
		t.Errorf("Rollback() promoted %q, want a new promotion rather than one the history already records", id)
	}
	if build := promoted.GetBuilds()["web"]; build != buildIdentity(0) {
		t.Errorf("Rollback() promoted web build %q, want %q, the build p1 promoted", build, buildIdentity(0))
	}
	if promoted.GetFlipBound() == nil {
		t.Error("Rollback() reported no flip bound, so nothing tells the user how long the flip takes")
	}

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range listed.GetPromotions() {
		ids = append(ids, entry.GetPromotion().GetPromotionId())
	}
	if want := []string{promoted.GetPromotionId(), "p2", "p1"}; !slices.Equal(ids, want) {
		t.Errorf("after the rollback the history reads %v, want %v", ids, want)
	}
	if !listed.GetPromotions()[0].GetActive() {
		t.Error("after the rollback the pointer does not name the promotion it made")
	}
}

func relayPlane(provider *fake.Provider) *fake.DataPlane {
	return provider.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
}

func TestTheDeployFlipSpeaksThroughThePromotionStagesOwnProgress(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	const marker = "the flip said this through the reporter it was handed"
	plane := relayPlane(provider)
	plane.SayOnFlip(marker)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	flipped := plane.FlipProgress()
	if flipped == nil {
		t.Fatal("the deploy never reached the router's flip, so nothing was reported from it")
	}
	if flipped == progress.DiscardProgress() {
		t.Fatal("the deploy handed the flip a discarding reporter, want the Promotion stage's own")
	}

	titles := map[string]string{}
	parents := map[string]string{}
	var spoke string
	for _, scope := range startedScopes(events) {
		titles[scope.id] = scope.title
		parents[scope.id] = scope.parent
	}
	for _, event := range events {
		if saidLine(event) == marker {
			spoke = string(event.GetSpanId())
		}
	}
	if spoke == "" {
		t.Fatal("nothing the flip said reached the stream, so the flip reports through a reporter the run does not have")
	}
	promotion := "Switching traffic to promotion " + result.GetPromotionId()
	if titles[spoke] != promotion || parents[spoke] != "" {
		t.Errorf("the flip spoke on stage %q, want the unit %q", titles[spoke], promotion)
	}
}

func TestADeployAnotherPromoteOvertookWhileItBuiltIsRefusedBusyAndFlipsNothing(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1")
	var overtook sync.Once
	vendor.FakeStacks().Entering(func(provider.StackSpec) error {
		overtook.Do(func() {
			promotion := router.Promotion{PromotionID: "p2", Builds: map[string]string{"web": buildIdentity(0)}}
			if _, err := releases.Promote(context.Background(), promotion, "", "p1"); err != nil {
				t.Errorf("the promote that overtook the deploy = %v", err)
			}
		})
		return nil
	})
	flipsBefore := relayPlane(vendor).Builds("shop", environment.TierProduction, router.DefaultPointer)

	result, _ := deploy(t, client, deployRequest())

	if result.GetSuccess() {
		t.Fatal("Deploy() overtaken while it built = success, want it refused")
	}
	for _, want := range []string{"p1", "p2", "ocel deploy"} {
		if !strings.Contains(result.GetError(), want) {
			t.Errorf("Deploy() overtaken while it built said %q, want it to name %q", result.GetError(), want)
		}
	}
	if active, err := releases.ActivePromotionID(context.Background(), ""); err != nil || active != "p2" {
		t.Errorf("the pointer names %q, %v, want p2, the promote that overtook the deploy", active, err)
	}
	if flipped := relayPlane(vendor).Builds("shop", environment.TierProduction, router.DefaultPointer); !maps.Equal(flipped, flipsBefore) {
		t.Errorf("the router serves %v after the refused deploy, want %v: a refused deploy flips nothing", flipped, flipsBefore)
	}
}

func overtakenWhileItBuilds(t *testing.T, vendor *fake.Provider, releases *ledger.Ledger) {
	t.Helper()
	var overtook sync.Once
	vendor.FakeStacks().Entering(func(provider.StackSpec) error {
		overtook.Do(func() {
			promotion := router.Promotion{PromotionID: "p2", Builds: map[string]string{"web": buildIdentity(0)}}
			if _, err := releases.Promote(context.Background(), promotion, "", "p1"); err != nil {
				t.Errorf("the promote that overtook the deploy = %v", err)
			}
		})
		return nil
	})
}

func stagedRecordKeys(t *testing.T, vendor *fake.Provider) []string {
	t.Helper()
	stored, err := vendor.KeyValues().List(context.Background(), ledger.Partition(environment.TierProduction, "shop"), "records")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, entry := range stored {
		keys = append(keys, ledger.RecordKey(entry.Key.Path[1], entry.Key.Path[2]))
	}
	return keys
}

func appStacksRecorded(t *testing.T, vendor *fake.Provider) []string {
	t.Helper()
	stacks, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, stack := range stacks {
		if !stack.Name.IsInfra() {
			names = append(names, stack.Name.String())
		}
	}
	return names
}

func TestADeployRefusedBusyAfterItProvisionedReclaimsWhatItProvisioned(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1")
	overtakenWhileItBuilds(t, vendor, releases)

	result, _ := deploy(t, client, deployRequest())

	if result.GetSuccess() {
		t.Fatal("Deploy() overtaken while it built = success, want it refused")
	}
	if !strings.Contains(result.GetError(), "p2") {
		t.Errorf("Deploy() said %q, want the refusal that stopped it, naming p2", result.GetError())
	}
	destroyed := destroyedStacks(vendor)
	if !slices.ContainsFunc(destroyed, func(stack string) bool { return strings.HasPrefix(stack, "prod--web--") }) {
		t.Errorf("the refused deploy destroyed %v, want the web stack it provisioned: no promotion names its build, so nothing else ever would", destroyed)
	}
	if left := appStacksRecorded(t, vendor); len(left) != 0 {
		t.Errorf("the refused deploy left stack records %v, want none", left)
	}
	if want := []string{ledger.RecordKey("web", buildIdentity(0))}; !slices.Equal(stagedRecordKeys(t, vendor), want) {
		t.Errorf("the ledger holds records %v after the refused deploy, want only %v: the record it staged names a build no promotion does", stagedRecordKeys(t, vendor), want)
	}
}

func TestADeployThatFailsAfterProvisioningRemovesTheFunctionsItsStackHolds(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	var removed []string
	var mu sync.Mutex
	vendor.ResourceStacks(resources.Hooks{Functions: &resources.FunctionHooks{
		Provision: vendor.ProvisionFunctions,
		Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Progress) error {
			mu.Lock()
			defer mu.Unlock()
			for _, function := range functions {
				removed = append(removed, function.Name)
			}
			return nil
		},
	}})
	vendor.WithHooks(func(hooks *provider.Hooks) {
		hooks.WarmFunctions = func(context.Context, []string, progress.Progress) error {
			return errors.New("the function never answered its warm-up")
		}
	})
	req := deployRequest()
	req.Manifest.Resources, req.Manifest.Usages = nil, nil

	result, _ := deploy(t, client, req)

	if result.GetSuccess() || !strings.Contains(result.GetError(), "the function never answered its warm-up") {
		t.Fatalf("Deploy() whose warm-up failed = %v, %q, want it failed with the warm-up's reason", result.GetSuccess(), result.GetError())
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(removed, "server") {
		t.Errorf("the failed deploy removed functions %v, want the server function its stack provisioned", removed)
	}
	if left := appStacksRecorded(t, vendor); len(left) != 0 {
		t.Errorf("the failed deploy left stack records %v, want none", left)
	}
}

func TestADeployWhoseOwnReclaimFailsStillSaysWhyItFailedAndWarnsOfTheReclaim(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1")
	overtakenWhileItBuilds(t, vendor, releases)
	vendor.FakeStacks().RefuseNextDestroy(errors.New("the stack is locked by another run"))

	result, events := deploy(t, client, deployRequest())

	if result.GetSuccess() || !strings.Contains(result.GetError(), "p2") {
		t.Fatalf("Deploy() = %v, %q, want the busy refusal it failed with, not the reclaim that followed", result.GetSuccess(), result.GetError())
	}
	if warned := strings.Join(warnings(events), "\n"); !strings.Contains(warned, "the stack is locked by another run") {
		t.Errorf("the deploy warned %q, want the reclaim that failed named", warned)
	}
	if left := appStacksRecorded(t, vendor); len(left) != 1 {
		t.Errorf("the deploy whose stack could not be destroyed left stack records %v, want its own kept for a later teardown", left)
	}
}

func TestADeployWhosePromotionTheLedgerStillNamesKeepsItsStacksWhenItFails(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	relayPlane(vendor).FailNextFlip(errors.New("the data plane refused the write"))

	result, _ := deploy(t, client, deployRequest())

	if result.GetSuccess() {
		t.Fatal("Deploy() with a router that refused its flip = success, want it to fail")
	}
	if destroyed := destroyedStacks(vendor); len(destroyed) != 0 {
		t.Errorf("the deploy destroyed %v, want its stacks kept: the ledger still records its promotion, and a later reclaim of that promotion owns them", destroyed)
	}
}

func TestADeployWhoseProvisionFailsAndCannotBeReclaimedKeepsItsStackOnRecord(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.App == nil {
			return nil
		}
		return errors.New("the stack half provisioned before the cloud refused the rest")
	})
	vendor.FakeStacks().RefuseNextDestroy(errors.New("the stack is locked by another run"))

	result, _ := deploy(t, client, deployRequest())

	if result.GetSuccess() || !strings.Contains(result.GetError(), "half provisioned") {
		t.Fatalf("Deploy() = %v, %q, want the provision's failure", result.GetSuccess(), result.GetError())
	}
	if left := appStacksRecorded(t, vendor); len(left) != 1 || !strings.HasPrefix(left[0], "prod--web--") {
		t.Errorf("the deploy left stack records %v, want the web stack it began to provision kept on record, so a teardown still destroys what it half made", left)
	}
}

func TestADeployWhoseSharedProvisionFailsRemovesWhatItsVendorNamed(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	var mu sync.Mutex
	var removed []string
	vendor.ResourceStacks(resources.Hooks{Functions: &resources.FunctionHooks{
		Provision: func(context.Context, provider.StackSpec, progress.Progress) ([]provider.Function, error) {
			return nil, errors.New("the revision never became ready")
		},
		Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Progress) error {
			mu.Lock()
			defer mu.Unlock()
			for _, function := range functions {
				removed = append(removed, function.Physical)
			}
			return nil
		},
		Shared: &resources.SharedHooks[provider.Function]{
			Name: func(context.Context, provider.StackSpec) ([]provider.Function, error) {
				return []provider.Function{{Name: "server", Physical: "shop-web-server"}}, nil
			},
			RemoveRevisions: func(context.Context, provider.StackRef, []provider.Function, progress.Progress) ([]provider.Function, error) {
				return nil, nil
			},
		},
	}})
	req := deployRequest()
	req.Manifest.Resources, req.Manifest.Usages = nil, nil

	result, _ := deploy(t, client, req)

	if result.GetSuccess() {
		t.Fatal("Deploy() whose provision failed = success, want it failed")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(removed, []string{"shop-web-server"}) {
		t.Errorf("the failed deploy removed %v, want the service its vendor named before provisioning: nothing else holds it", removed)
	}
	if left := appStacksRecorded(t, vendor); len(left) != 0 {
		t.Errorf("the failed deploy left stack records %v, want none", left)
	}
}

func TestADeployWhoseCallerHungUpStillReclaimsWhatItProvisioned(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	removedUnder := make(chan error, 1)
	vendor.ResourceStacks(resources.Hooks{Functions: &resources.FunctionHooks{
		Provision: vendor.ProvisionFunctions,
		Remove: func(ctx context.Context, _ provider.StackRef, _ []provider.Function, _ progress.Progress) error {
			removedUnder <- ctx.Err()
			return nil
		},
	}})
	ctx, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	vendor.WithHooks(func(hooks *provider.Hooks) {
		hooks.WarmFunctions = func(ctx context.Context, _ []string, _ progress.Progress) error {
			hangUp()
			<-ctx.Done()
			return ctx.Err()
		}
	})
	req := deployRequest()
	req.Manifest.Resources, req.Manifest.Usages = nil, nil

	stream, err := client.Deploy(ctx, req)
	if err == nil {
		for stream.Receive() {
		}
		_ = stream.Close()
	}

	select {
	case err := <-removedUnder:
		if err != nil {
			t.Errorf("the reclaim removed the deploy's functions under a context that was already %v, want one the hang-up did not cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the deploy whose caller hung up never reclaimed the functions it provisioned")
	}
}

func TestTheRollbackFlipIsHandedProgressThatDiscards(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")
	plane := relayPlane(provider)
	plane.SayOnFlip("the flip said this into a rollback that streams nothing")

	if _, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p1"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	flipped := plane.FlipProgress()
	if flipped == nil {
		t.Fatal("the rollback never reached the router's flip")
	}
	if flipped != progress.DiscardProgress() {
		t.Errorf("the rollback handed the flip %#v, want the discarding reporter: Rollback is a unary RPC that streams nothing", flipped)
	}
}

func seedTakenBack(t *testing.T, provider *fake.Provider) {
	t.Helper()
	releases := seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1")
	for i, id := range []string{"p2", "p3"} {
		build := buildIdentity(i + 1)
		if err := releases.PutStaged(context.Background(), router.DeploymentRecord{App: "web", Build: build}); err != nil {
			t.Fatal(err)
		}
		promotion := router.Promotion{PromotionID: id, Ts: int64(i + 2), Builds: map[string]string{"web": build}}
		if _, err := releases.Promote(context.Background(), promotion, "", "p1"); err != nil {
			t.Fatal(err)
		}
		if id == "p2" {
			if err := releases.Unpromote(context.Background(), "p2", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestListPromotionsMarksAPromotionTakenBack(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedTakenBack(t, provider)

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("ListPromotions() error = %v", err)
	}
	for _, entry := range listed.GetPromotions() {
		if want := entry.GetPromotion().GetPromotionId() == "p2"; entry.GetUnpromoted() != want {
			t.Errorf("ListPromotions() marks %s unpromoted %v, want %v: only p2 was taken back", entry.GetPromotion().GetPromotionId(), entry.GetUnpromoted(), want)
		}
	}
}

func TestRollbackPassesOverAPromotionTakenBack(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedTakenBack(t, provider)

	rolled, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if build := rolled.GetPromoted().GetBuilds()["web"]; build != buildIdentity(0) {
		t.Errorf("Rollback() promoted web build %q, want %q, the build p1 served: p2 was taken back and never served", build, buildIdentity(0))
	}
}

func TestRollbackToAPromotionTakenBackIsRefused(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedTakenBack(t, provider)

	_, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p2"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "p2") {
		t.Fatalf("Rollback() to p2 = %v, want it refused as an invalid argument naming p2, which was taken back", err)
	}
}

func TestRollbackRefusesAPromotionTheHistoryDoesNotContain(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1")

	_, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p9"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Rollback() = %v, want it refused as an invalid argument", err)
	}
}

func TestARollbackWhosePointerWriteLostToAChangeThatLeftItsPromotionActiveLands(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")

	pointer := ledger.Partition(environment.TierProduction, "shop").Key("pointers", router.DefaultPointer)
	provider.KeyValues().(*fake.KeyValues).MoveBeforeNextWrite(pointer)

	rolled, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop", To: "p1"})
	if err != nil {
		t.Fatalf("Rollback() against a pointer rewritten with p2 still active = %v, want it to land", err)
	}
	active, err := ledger.New(provider.KeyValues(), environment.TierProduction, "shop").ActivePromotionID(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if active != rolled.GetPromoted().GetPromotionId() {
		t.Errorf("the pointer names %q, want %q, the promotion the rollback made", active, rolled.GetPromoted().GetPromotionId())
	}
}

func TestChangingAProjectsEdgeKeepsItsPromotionHistory(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	first, _ := deploy(t, client, deployRequest())
	if first == nil || !first.GetSuccess() {
		t.Fatalf("Deploy() through the relay edge = %q, want it to succeed", first.GetError())
	}
	moved := deployRequest()
	moved.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	second, _ := deploy(t, client, moved)
	if second == nil || !second.GetSuccess() {
		t.Fatalf("Deploy() through the direct edge = %q, want it to succeed", second.GetError())
	}

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{
		Slug: "shop",
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
	})
	if err != nil {
		t.Fatalf("ListPromotions() error = %v", err)
	}
	var ids []string
	for _, entry := range listed.GetPromotions() {
		ids = append(ids, entry.GetPromotion().GetPromotionId())
	}
	if !slices.Contains(ids, first.GetPromotionId()) || !slices.Contains(ids, second.GetPromotionId()) {
		t.Errorf("ListPromotions() after the edge changed = %v, want both %s (through relay) and %s (through direct): the history belongs to the project, not to the edge it deploys through",
			ids, first.GetPromotionId(), second.GetPromotionId())
	}
}

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
		{"when nothing was ever deployed", 2, false, "Nothing to prune: shop has no deploy in production"},
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
			if !slices.Contains(said, tc.wants) || (tc.name == "past the newest two" && !slices.Contains(said, "Destroying the stack of web build 00000000000000000000000000000001~000000000001 (1 of 1)")) {
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
		Provision: func(context.Context, provider.StackSpec, progress.Progress) ([]provider.Function, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.revisions++
			return []provider.Function{{Name: "api", Physical: "shop-web-api", Revision: fmt.Sprintf("shop-web-api-%05d", s.revisions)}}, nil
		},
		Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Progress) error {
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
			RemoveRevisions: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Progress) ([]provider.Function, error) {
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
			Name:    naming.AppStack(stackrecords.ProductionEnv, "web", releaseOf(t, buildIdentity(i))),
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
			Build:     buildIdentity(i),
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

	if _, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if _, found, err := releases.Record(context.Background(), "web", buildIdentity(0)); err != nil || found {
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

	if _, found, err := releases.Record(context.Background(), "web", buildIdentity(0)); err != nil || found {
		t.Errorf("the record of p00's build = found %v, %v, want it removed once no promotion names it", found, err)
	}
	want := fmt.Sprintf("Destroying the stack of web build %s (1 of 1)", buildIdentity(0))
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
	for _, want := range []string{result.GetPromotionId(), "the stack is locked by another run"} {
		if !strings.Contains(warned, want) {
			t.Errorf("the deploy warned %q, want it to name %q", warned, want)
		}
	}
	if _, found, err := releases.Record(context.Background(), "web", buildIdentity(0)); err != nil || !found {
		t.Errorf("the record of p00's build = found %v, %v, want it kept: its stack was not destroyed", found, err)
	}
}

func TestADeployWhoseDroppedRecordCannotBeRemovedStillServesItsPromotion(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedKept(t, vendor)
	record := ledger.Partition(environment.TierProduction, "shop").Key("records", "web", buildIdentity(0))
	vendor.KeyValues().(*fake.KeyValues).SetRemovalError(record, errors.New("the table refused the delete"))

	result, events := deploy(t, client, deployRequest())

	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() whose dropped record could not be removed = %q, want it to succeed", result.GetError())
	}
	active, found, err := releases.ReadActive(context.Background(), "")
	if err != nil || !found || active.PromotionID != result.GetPromotionId() {
		t.Fatalf("the ledger names %+v, %v, %v, want %s", active, found, err, result.GetPromotionId())
	}
	if served := relayPlane(vendor).Builds("shop", environment.TierProduction, router.DefaultPointer)["web"]; served != active.Builds["web"] {
		t.Errorf("the router serves web %q, want %q, the build the ledger names", served, active.Builds["web"])
	}
	if !strings.Contains(strings.Join(warnings(events), "\n"), "the table refused the delete") {
		t.Errorf("the deploy warned %q, want the removal that failed named", warnings(events))
	}
}

func TestADeployARouterLeftUnservedStillReclaimsTheBuildItsPromoteDropped(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedKept(t, vendor)
	relayPlane(vendor).FailNextFlip(errors.New("the data plane refused the write"))

	result, _ := deploy(t, client, deployRequest())

	if result.GetSuccess() {
		t.Fatal("Deploy() with a router that refused its flip = success, want it to fail")
	}
	if _, found, err := releases.Record(context.Background(), "web", buildIdentity(0)); err != nil || found {
		t.Errorf("the record of p00's build = found %v, %v, want it reclaimed: the promote dropped p00 whether or not it served", found, err)
	}
	if !slices.ContainsFunc(vendor.Journal(), func(entry string) bool { return strings.HasPrefix(entry, "destroy ") }) {
		t.Errorf("the journal reads %v, want the stack of p00's build destroyed", vendor.Journal())
	}
}

func TestARollbackWhoseDroppedBuildCannotBeReclaimedServesAndWarns(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	seedKept(t, vendor)
	vendor.FakeStacks().RefuseNextDestroy(errors.New("the stack is locked by another run"))

	rolled, err := client.Rollback(context.Background(), &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() whose reclaim failed = %v, want it to succeed: its promotion serves", err)
	}
	if warned := strings.Join(rolled.GetWarnings(), "\n"); !strings.Contains(warned, "the stack is locked by another run") {
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
		builds := map[string]string{"web": buildIdentity(i)}
		if i == 0 {
			builds["api"] = "garbage"
		}
		for app, build := range builds {
			if err := releases.PutStaged(ctx, router.DeploymentRecord{App: app, Build: build}); err != nil {
				t.Fatal(err)
			}
		}
		promotion := router.Promotion{PromotionID: fmt.Sprintf("p%02d", i), Ts: int64(i + 1), Builds: builds}
		if _, err := releases.Promote(ctx, promotion, "", replaces); err != nil {
			t.Fatal(err)
		}
		replaces = promotion.PromotionID
	}

	rolled, err := client.Rollback(ctx, &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if _, found, err := releases.Record(ctx, "web", buildIdentity(0)); err != nil || found {
		t.Errorf("the record of p00's web build = found %v, %v, want it reclaimed beside the record it could not read", found, err)
	}
	if _, found, err := releases.Record(ctx, "api", "garbage"); err != nil || !found {
		t.Errorf("the record naming no build = found %v, %v, want it kept: nothing names the stack it would free", found, err)
	}
	if warned := strings.Join(rolled.GetWarnings(), "\n"); !strings.Contains(warned, "garbage") {
		t.Errorf("Rollback() warned %q, want the record it could not reclaim named", warned)
	}
}

func seedKeptContainers(t *testing.T, vendor *fake.Provider) *ledger.Ledger {
	t.Helper()
	releases := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop")
	ctx := context.Background()
	replaces := ""
	for i := range ledger.KeptPromotions {
		record := router.DeploymentRecord{App: "web", Build: buildIdentity(i), Image: containerTestImage, Physical: fmt.Sprintf("web-%02d", i)}
		if err := releases.PutStaged(ctx, record); err != nil {
			t.Fatal(err)
		}
		promotion := router.Promotion{PromotionID: fmt.Sprintf("p%02d", i), Ts: int64(i + 1), Builds: map[string]string{"web": record.Build}}
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

	if _, err := client.Rollback(ctx, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	want := naming.AppStack(stackrecords.ProductionEnv, "web", releaseOf(t, buildIdentity(0))).String()
	if destroyed := destroyedStacks(vendor); !slices.Contains(destroyed, want) {
		t.Errorf("the rollback destroyed %v, want %s: a container release this provider keeps no window of is reclaimed like any other", destroyed, want)
	}
	if _, found, err := releases.Record(ctx, "web", buildIdentity(0)); err != nil || found {
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

	if _, err := client.Rollback(ctx, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if destroyed := destroyedStacks(vendor); len(destroyed) != 0 {
		t.Errorf("the rollback destroyed %v, want the container build it dropped left to the provider that keeps its own window of them", destroyed)
	}
	if _, found, err := releases.Record(ctx, "web", buildIdentity(0)); err != nil || found {
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
		record, staged, err := releases.Record(ctx, "web", active.Builds["web"])
		if err != nil || !staged {
			t.Fatalf("the record of web build %s = %v, %v, want it staged", active.Builds["web"], staged, err)
		}
		return active.Builds["web"], record.Origin
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

	if _, err := client.Rollback(ctx, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if build, origin := served(); build != builds[0] || origin != origins[0] {
		t.Errorf("after the rollback web serves build %s at %s, want build %s at %s, the origin the first deploy provisioned", build, origin, builds[0], origins[0])
	}
}
