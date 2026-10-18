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
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func seedPromotions(t *testing.T, provider *fake.Provider, tier environment.Tier, slug, pointer string, ids ...string) *ledger.Ledger {
	t.Helper()
	releases := ledger.New(provider.KeyValues(), tier, slug)
	replaces := ""
	for i, id := range ids {
		if err := releases.PutStaged(context.Background(), router.ReleaseRecord{App: "web", Release: releaseFor(i)}); err != nil {
			t.Fatal(err)
		}
		promotion := router.Promotion{PromotionID: id, Ts: int64(i + 1), Releases: map[string]string{"web": releaseFor(i)}}
		if _, err := releases.Promote(context.Background(), promotion, pointer, replaces); err != nil {
			t.Fatal(err)
		}
		replaces = id
	}
	return releases
}

func releaseFor(seq int) string {
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

	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if id := rolled.promotionID(); id == "p1" || id == "p2" {
		t.Errorf("Rollback() promoted %q, want a new promotion rather than one the history already records", id)
	}
	if build := rolled.release("web"); build != releaseFor(0) {
		t.Errorf("Rollback() promoted web build %q, want %q, the build p1 promoted", build, releaseFor(0))
	}
	if rolled.result.GetPropagation() == nil {
		t.Error("Rollback() reported no propagation, so nothing tells the user how long the pointer move takes")
	}

	listed, err := client.ListPromotions(context.Background(), &contractv1.ListPromotionsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range listed.GetPromotions() {
		ids = append(ids, entry.GetPromotion().GetPromotionId())
	}
	if want := []string{rolled.promotionID(), "p2", "p1"}; !slices.Equal(ids, want) {
		t.Errorf("after the rollback the history reads %v, want %v", ids, want)
	}
	if !listed.GetPromotions()[0].GetActive() {
		t.Error("after the rollback the pointer does not name the promotion it made")
	}
}

func TestARollbackIsRefusedWhileADeployHoldsProduction(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")
	takeLeaseOver(t, provider.KeyValues(), otherEnvironmentLease)

	_, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"})

	if err == nil || !strings.Contains(err.Error(), "a deploy to prod is running: roll back again once it ends") {
		t.Fatalf("Rollback() = %v, want it refused while a deploy holds production", err)
	}
	active, err := ledger.New(provider.KeyValues(), environment.TierProduction, "shop").ActivePromotionID(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if active != "p2" {
		t.Errorf("the pointer names %q after the refused rollback, want p2", active)
	}
}

func TestARollbackHoldsProductionWhileItMovesThePointerAndFreesItAfter(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")
	var during error
	relayPlane(provider).BeforeNextPointerMove(func() {
		_, during = stackrecords.TakeEnvironmentLease(context.Background(), provider.KeyValues(), environment.TierProduction, "shop", stackrecords.ProductionEnv,
			otherEnvironmentLease, stackrecords.LeaseDeploy, stackrecords.LeaseTerms{TTL: time.Minute, Now: time.Now})
	})

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	if during == nil || !strings.Contains(during.Error(), "a rollback of prod is running") {
		t.Errorf("a deploy taking production while the rollback moved the pointer = %v, want it refused because the rollback holds production", during)
	}
	if _, err := provider.KeyValues().Read(context.Background(), stackrecords.EnvironmentLeaseKey(environment.TierProduction, "shop", stackrecords.ProductionEnv)); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("reading production's lease after the rollback = %v, want it freed", err)
	}
}

func relayPlane(provider *fake.Provider) *fake.DataPlane {
	return provider.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
}

func TestTheDeployPointerMoveSpeaksThroughThePromotionSpansOwnProgress(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	const marker = "the pointer move said this through the reporter it was handed"
	plane := relayPlane(provider)
	plane.SayOnPointerMove(marker)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	moved := plane.PointerMoveProgress()
	if moved == nil {
		t.Fatal("the deploy never reached the router's pointer move, so nothing was reported from it")
	}
	if moved == progress.Discard() {
		t.Fatal("the deploy handed the pointer move a discarding reporter, want the Promotion span's own")
	}

	titles := map[string]string{}
	parents := map[string]string{}
	var spoke string
	for _, scope := range startedSpans(events) {
		titles[scope.id] = scope.title
		parents[scope.id] = scope.parent
	}
	for _, event := range events {
		if saidLine(event) == marker {
			spoke = string(event.GetSpanId())
		}
	}
	if spoke == "" {
		t.Fatal("nothing the pointer move said reached the stream, so the pointer move reports through a reporter the run does not have")
	}
	promotion := "Switching traffic to promotion " + result.GetPromotionId()
	if titles[spoke] != promotion || parents[spoke] != "" {
		t.Errorf("the pointer move spoke on span %q, want the span %q", titles[spoke], promotion)
	}
}

func TestADeployAnotherPromoteOvertookWhileItBuiltIsRefusedBusyAndMovesNothing(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	releases := seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1")
	var overtook sync.Once
	vendor.FakeStacks().Entering(func(provider.StackSpec) error {
		overtook.Do(func() {
			promotion := router.Promotion{PromotionID: "p2", Releases: map[string]string{"web": releaseFor(0)}}
			if _, err := releases.Promote(context.Background(), promotion, "", "p1"); err != nil {
				t.Errorf("the promote that overtook the deploy = %v", err)
			}
		})
		return nil
	})
	servedBefore := relayPlane(vendor).Releases("shop", environment.TierProduction, router.DefaultPointer)

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
	if moved := relayPlane(vendor).Releases("shop", environment.TierProduction, router.DefaultPointer); !maps.Equal(moved, servedBefore) {
		t.Errorf("the router serves %v after the refused deploy, want %v: a refused deploy moves nothing", moved, servedBefore)
	}
}

func overtakenWhileItBuilds(t *testing.T, vendor *fake.Provider, releases *ledger.Ledger) {
	t.Helper()
	var overtook sync.Once
	vendor.FakeStacks().Entering(func(provider.StackSpec) error {
		overtook.Do(func() {
			promotion := router.Promotion{PromotionID: "p2", Releases: map[string]string{"web": releaseFor(0)}}
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
	if want := []string{ledger.RecordKey("web", releaseFor(0))}; !slices.Equal(stagedRecordKeys(t, vendor), want) {
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
		Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ provider.ImageStore, _ progress.Log) error {
			mu.Lock()
			defer mu.Unlock()
			for _, function := range functions {
				removed = append(removed, function.Name)
			}
			return nil
		},
	}})
	vendor.KeyValues().(*fake.KeyValues).BeforeNextWriteUnder(ledger.Partition(environment.TierProduction, "shop"), "records/", func() error {
		return errors.New("the ledger refused the staged record")
	})
	req := deployRequest()
	req.Manifest.Resources, req.Manifest.Usages = nil, nil

	result, _ := deploy(t, client, req)

	if result.GetSuccess() || !strings.Contains(result.GetError(), "the ledger refused the staged record") {
		t.Fatalf("Deploy() whose staged record was refused = %v, %q, want it failed with the ledger's reason", result.GetSuccess(), result.GetError())
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
	relayPlane(vendor).FailNextPointerMove(errors.New("the data plane refused the write"))

	result, _ := deploy(t, client, deployRequest())

	if result.GetSuccess() {
		t.Fatal("Deploy() with a router that refused its pointer move = success, want it to fail")
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
		Provision: func(context.Context, provider.StackSpec, progress.Log) ([]provider.Function, error) {
			return nil, errors.New("the revision never became ready")
		},
		Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ provider.ImageStore, _ progress.Log) error {
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
			RemoveRevisions: func(context.Context, provider.StackRef, []provider.Function, provider.ImageStore, progress.Log) ([]provider.Function, error) {
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
		Remove: func(ctx context.Context, _ provider.StackRef, _ []provider.Function, _ provider.ImageStore, _ progress.Log) error {
			removedUnder <- ctx.Err()
			return nil
		},
	}})
	ctx, hangUp := context.WithCancel(context.Background())
	defer hangUp()
	vendor.KeyValues().(*fake.KeyValues).BeforeNextWriteUnder(ledger.Partition(environment.TierProduction, "shop"), "records/", func() error {
		hangUp()
		return context.Canceled
	})
	req := deployRequest()
	req.Manifest.Resources, req.Manifest.Usages = nil, nil

	stream, err := client.Deploy(ctx, rooted(req))
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

func TestTheRollbackPointerMoveSpeaksThroughTheRollbacksOwnSpan(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1", "p2")
	const marker = "the pointer move said this through the reporter it was handed"
	relayPlane(provider).SayOnPointerMove(marker)

	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop", To: "p1"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	titles := map[string]string{}
	for _, scope := range startedSpans(rolled.events) {
		titles[scope.id] = scope.title
	}
	spoke, said := "", false
	for _, event := range rolled.events {
		if saidLine(event) == marker {
			spoke, said = titles[string(event.GetSpanId())], true
		}
	}
	if !said {
		t.Fatal("nothing the pointer move said reached the stream, so a rollback reports nothing of the pointer move it waits on")
	}
	if want := "Switching production traffic back to promotion p1"; spoke != want {
		t.Errorf("the pointer move spoke on span %q, want %q", spoke, want)
	}
}

type rolledBack struct {
	result *progressv1.OperationResult
	events []*progressv1.OperationEvent
}

func (r rolledBack) promotionID() string { return r.result.GetPromotionId() }

func (r rolledBack) release(app string) string {
	for _, outcome := range r.result.GetApps() {
		if outcome.GetApp() == app {
			return outcome.GetRelease()
		}
	}
	return ""
}

func (r rolledBack) warnings() []string { return said(r.events, progressv1.Level_LEVEL_WARN) }

func rollBack(ctx context.Context, client contractv1connect.ProviderServiceClient, req *contractv1.RollbackRequest) (rolledBack, error) {
	stream, err := client.Rollback(ctx, req)
	if err != nil {
		return rolledBack{}, err
	}
	rolled := rolledBack{events: recorded(stream)}
	if err := stream.Err(); err != nil {
		return rolled, err
	}
	for _, event := range rolled.events {
		if result := event.GetResult(); result != nil {
			rolled.result = result
		}
	}
	if !rolled.result.GetSuccess() {
		return rolled, fmt.Errorf("rollback failed: %s", rolled.result.GetError())
	}
	return rolled, nil
}

func seedTakenBack(t *testing.T, provider *fake.Provider) {
	t.Helper()
	releases := seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1")
	for i, id := range []string{"p2", "p3"} {
		build := releaseFor(i + 1)
		if err := releases.PutStaged(context.Background(), router.ReleaseRecord{App: "web", Release: build}); err != nil {
			t.Fatal(err)
		}
		promotion := router.Promotion{PromotionID: id, Ts: int64(i + 2), Releases: map[string]string{"web": build}}
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

	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if build := rolled.release("web"); build != releaseFor(0) {
		t.Errorf("Rollback() promoted web build %q, want %q, the build p1 served: p2 was taken back and never served", build, releaseFor(0))
	}
}

func TestRollbackToAPromotionTakenBackIsRefused(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedTakenBack(t, provider)

	_, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop", To: "p2"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "p2") {
		t.Fatalf("Rollback() to p2 = %v, want it refused as an invalid argument naming p2, which was taken back", err)
	}
}

func TestRollbackRefusesAPromotionTheHistoryDoesNotContain(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	edgeProvisioned(t, provider, environment.TierProduction, "shop")
	seedPromotions(t, provider, environment.TierProduction, "shop", "", "p1")

	_, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop", To: "p9"})
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

	rolled, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop", To: "p1"})
	if err != nil {
		t.Fatalf("Rollback() against a pointer rewritten with p2 still active = %v, want it to land", err)
	}
	active, err := ledger.New(provider.KeyValues(), environment.TierProduction, "shop").ActivePromotionID(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if active != rolled.promotionID() {
		t.Errorf("the pointer names %q, want %q, the promotion the rollback made", active, rolled.promotionID())
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
