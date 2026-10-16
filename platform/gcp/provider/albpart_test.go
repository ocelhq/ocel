package gcp

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func deployingBehindALB(slug string) provider.DeployPreflight {
	return provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: slug}, Edge: alb.Kind}
}

func TestTheFirstDeployBehindTheALBAddsItsLoadBalancerOnceAndEveryProjectBehindItUsesIt(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction

	for _, slug := range []string{"shop", "blog", "shop"} {
		if err := b.addParts(ctx, deployingBehindALB(slug), []string{albPart}); err != nil {
			t.Fatalf("addParts(%s) = %v", slug, err)
		}
	}

	if !slices.Equal(front.raised, []environment.Tier{tier}) {
		t.Errorf("the load balancer was raised for %v, want once for the tier", front.raised)
	}
	parts, err := b.listDescribedParts(ctx, tier)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(parts, func(part provider.BootstrapPart) bool { return part.Name == albPart })
	if at < 0 {
		t.Fatalf("listDescribedParts() = %+v, want the %s part", parts, albPart)
	}
	if got := parts[at]; !got.RetiresWithLastUser || !slices.Equal(got.UsedBy, []string{"blog", "shop"}) || got.Shape == nil || len(got.Shape.Resources) == 0 {
		t.Errorf("the %s part = %+v, want it used by blog and shop, priced, and retiring with its last user", albPart, got)
	}
}

func frontingALB(t *testing.T) (bootstrap, *countingFront) {
	t.Helper()
	b, _ := servingNetworks(t)
	b.records = fake.NewKeyValues()
	return b, b.fronts.(*frontRegistry).front
}

func TestTheLoadBalancerIsTakenDownWithTheLastProjectBehindItAndNotBefore(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	for _, slug := range []string{"shop", "blog"} {
		if err := b.addParts(ctx, deployingBehindALB(slug), []string{albPart}); err != nil {
			t.Fatal(err)
		}
	}

	if err := b.releaseALB(ctx, tier, "shop", true, nil); err != nil {
		t.Fatalf("releaseALB(shop) = %v", err)
	}
	if len(front.torn) != 0 {
		t.Fatalf("the load balancer was taken down while blog still deploys behind it")
	}
	if err := b.releaseALB(ctx, tier, "blog", true, nil); err != nil {
		t.Fatalf("releaseALB(blog) = %v", err)
	}
	if !slices.Equal(front.torn, []environment.Tier{tier}) {
		t.Errorf("the load balancer was taken down for %v, want once its last project left", front.torn)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, albPart); err != nil || present {
		t.Errorf("the %s part record outlived its load balancer: %t, %v", albPart, present, err)
	}

	if err := b.addParts(ctx, deployingBehindALB("docs"), []string{albPart}); err != nil {
		t.Fatalf("addParts(docs) after the retirement = %v", err)
	}
	if len(front.raised) != 2 {
		t.Errorf("the next project behind the alb raised the load balancer %d times in all, want it raised again after the retirement", len(front.raised))
	}
}

func TestTheLastProjectLeavingKeepsTheLoadBalancerWhileAHostnameIsBoundToIt(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if err := b.addParts(ctx, deployingBehindALB("shop"), []string{albPart}); err != nil {
		t.Fatal(err)
	}
	front.bound = []string{"shop.example.com"}

	if err := b.releaseALB(ctx, tier, "shop", false, nil); err != nil {
		t.Fatalf("releaseALB() by a deploy moving off the alb = %v", err)
	}
	if users, _ := b.listALBUsers(ctx, tier); len(front.torn) != 0 || !slices.Equal(users, []string{"shop"}) {
		t.Errorf("a deploy moving off the alb while its hostname is bound tore down %v and left users %v, want the load balancer kept and shop still its user", front.torn, users)
	}
	if err := b.releaseALB(ctx, tier, "shop", true, nil); err != nil {
		t.Fatalf("releaseALB() by a destroy = %v", err)
	}
	if users, _ := b.listALBUsers(ctx, tier); len(front.torn) != 0 || len(users) != 0 {
		t.Errorf("a destroy while a hostname is bound tore down %v and left users %v, want the load balancer kept and the project forgotten", front.torn, users)
	}
}

func TestADeployBehindTheALBWhileItIsBeingTakenDownIsRefusedAsBusy(t *testing.T) {
	t.Parallel()
	b, _ := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if _, err := writeALBLease(ctx, b.records, keyvalue.Entry{Key: albLeaseAt(tier)}, albLease{Retiring: true, RetiringSince: time.Now()}); err != nil {
		t.Fatal(err)
	}

	err := b.addParts(ctx, deployingBehindALB("shop"), []string{albPart})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("addParts() while the load balancer goes down = %v, want a %s refusal", err, refusal.CodeBusy)
	}
	if users, _ := b.listALBUsers(ctx, tier); len(users) != 0 {
		t.Errorf("a refused claim left users %v, want none", users)
	}
}

func TestADryDeployBehindTheALBPlansItsLoadBalancerAndRaisesNothing(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	var planned []provider.ChangeGroup
	pre := deployingBehindALB("shop")
	pre.Dry = true
	pre.AddToPlan = func(groups ...provider.ChangeGroup) { planned = append(planned, groups...) }

	if err := b.addParts(context.Background(), pre, []string{albPart}); err != nil {
		t.Fatalf("addParts() = %v", err)
	}
	if len(front.raised) != 0 || len(planned) != 1 || planned[0].Feature != albPart || planned[0].Action != provider.ActionCreate {
		t.Errorf("a dry deploy raised %v and planned %+v, want nothing raised and the %s part planned", front.raised, planned, albPart)
	}
}

func TestADeployMayAddAndRetireTheALBsGateRepositoryAndTheBaseKeepsTheCDNPurgeRole(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}
	parts := newPartsRole(names).permissions
	for _, permission := range []string{"artifactregistry.repositories.create", "artifactregistry.repositories.delete"} {
		if !slices.Contains(parts, permission) {
			t.Errorf("the parts role holds %v, want %s: a deploy adds the gate's repository with the load balancer and the last destroy removes it", parts, permission)
		}
	}
	role := item{Kind: KindRole, Name: names.CDNPurgeRole()}
	if !slices.Contains(idsOf(bootstrapItems(names, environment.TierProduction, false)), role.ID()) {
		t.Errorf("the base provisions %v, want %s: a deploy grants Next apps behind the alb a role only a bootstrap may make", idsOf(bootstrapItems(names, environment.TierProduction, false)), role.ID())
	}
	if slices.ContainsFunc(bootstrap{}.Catalogue(), func(f provider.Feature) bool { return strings.Contains(f.Name, "alb-edge") }) {
		t.Errorf("Catalogue() = %v, want no alb-edge feature: the first deploy behind the alb adds its load balancer", bootstrap{}.Catalogue())
	}
}

type listingTogether struct {
	keyvalue.Store
	under   keyvalue.Key
	parties int

	mu      sync.Mutex
	arrived int
	all     chan struct{}
}

func (s *listingTogether) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	listed, err := s.Store.List(ctx, in, under...)
	if reflect.DeepEqual(in, s.under.Partition) && slices.Equal(under, s.under.Path) {
		s.mu.Lock()
		s.arrived++
		if s.arrived == s.parties {
			close(s.all)
		}
		s.mu.Unlock()
		select {
		case <-s.all:
		case <-time.After(2 * time.Second):
		}
	}
	return listed, err
}

func TestTwoLastProjectsLeavingTheALBAtOnceTakeItsLoadBalancerDownOnce(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	for _, slug := range []string{"shop", "blog"} {
		if err := b.addParts(ctx, deployingBehindALB(slug), []string{albPart}); err != nil {
			t.Fatal(err)
		}
	}
	b.records = &listingTogether{Store: b.records, under: albConsumers(tier), parties: 2, all: make(chan struct{})}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, slug := range []string{"shop", "blog"} {
		wg.Go(func() { errs[i] = b.releaseALB(ctx, tier, slug, true, nil) })
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("releaseALB() = %v", err)
	}
	if users, _ := b.listALBUsers(ctx, tier); !slices.Equal(front.torn, []environment.Tier{tier}) || len(users) != 0 {
		t.Errorf("two last projects leaving at once tore down %v and left users %v, want the load balancer taken down once", front.torn, users)
	}
}

func TestALoadBalancerWhoseTeardownFailedIsRaisedAgainByTheNextDeployBehindIt(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if err := b.addParts(ctx, deployingBehindALB("shop"), []string{albPart}); err != nil {
		t.Fatal(err)
	}
	front.refusal = errors.New("google refused to delete the url map")

	if err := b.releaseALB(ctx, tier, "shop", true, nil); err == nil {
		t.Fatal("releaseALB() with a failing teardown = nil, want the failure")
	}
	front.refusal = nil
	if err := b.addParts(ctx, deployingBehindALB("docs"), []string{albPart}); err != nil {
		t.Fatalf("addParts(docs) after a failed teardown = %v", err)
	}
	if len(front.raised) != 2 {
		t.Errorf("the load balancer was raised %d times, want it raised again by the deploy after the failed teardown", len(front.raised))
	}
}

func TestARetireCancelledMidwayLeavesTheNextDeployBehindTheALBUnrefused(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	tier := environment.TierProduction
	if err := b.addParts(context.Background(), deployingBehindALB("shop"), []string{albPart}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	front.tearing = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}

	if err := b.releaseALB(ctx, tier, "shop", true, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("releaseALB() cancelled during the teardown = %v, want the cancellation", err)
	}
	front.tearing = nil
	if err := b.addParts(context.Background(), deployingBehindALB("docs"), []string{albPart}); err != nil {
		t.Fatalf("addParts(docs) after a cancelled retire = %v, want the lease lifted and the load balancer raised again", err)
	}
	if len(front.raised) != 2 {
		t.Errorf("the load balancer was raised %d times, want it raised again after the cancelled retire", len(front.raised))
	}
}

func TestADeployBehindTheALBTakesOverARetireThatOutlivedItsTimeLimit(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	stale := albLease{Retiring: true, RetiringSince: time.Now().Add(-2 * albRetireLimit)}
	if _, err := writeALBLease(ctx, b.records, keyvalue.Entry{Key: albLeaseAt(environment.TierProduction)}, stale); err != nil {
		t.Fatal(err)
	}

	if err := b.addParts(ctx, deployingBehindALB("shop"), []string{albPart}); err != nil {
		t.Fatalf("addParts() with a retire older than %s = %v, want the deploy to take it over", albRetireLimit, err)
	}
	if len(front.raised) != 1 {
		t.Errorf("the load balancer was raised %d times, want it raised by the deploy that took the stale retire over", len(front.raised))
	}
}

func TestARefusedClaimOnTheALBKeepsTheUserRecordAnEarlierDeployWrote(t *testing.T) {
	t.Parallel()
	b, _ := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if err := b.addParts(ctx, deployingBehindALB("shop"), []string{albPart}); err != nil {
		t.Fatal(err)
	}
	leased, _, err := readALBLease(ctx, b.records, tier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeALBLease(ctx, b.records, leased, albLease{Retiring: true, RetiringSince: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := b.claimALB(ctx, tier, "shop"); refusalCode(err) != refusal.CodeBusy {
		t.Fatalf("claimALB() while the load balancer goes down = %v, want a %s refusal", err, refusal.CodeBusy)
	}
	if users, _ := b.listALBUsers(ctx, tier); !slices.Equal(users, []string{"shop"}) {
		t.Errorf("a refused claim left users %v, want shop, whose record an earlier deploy wrote", users)
	}
}

func TestRemovingAProjectWhoseOnlyALBDeployFailedTakesItsLoadBalancerDown(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if err := b.addParts(ctx, deployingBehindALB("shop"), []string{albPart}); err != nil {
		t.Fatal(err)
	}

	if err := b.removePartUser(ctx, tier, "shop", nil); err != nil {
		t.Fatalf("removePartUser() = %v", err)
	}
	if users, _ := b.listALBUsers(ctx, tier); !slices.Equal(front.torn, []environment.Tier{tier}) || len(users) != 0 {
		t.Errorf("removing shop tore down %v and left users %v, want the load balancer shop alone used taken down", front.torn, users)
	}
}
