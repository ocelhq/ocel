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
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func deployingBehindALB(slug string) provider.DeployPreflight {
	return provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: slug}, Edge: albresources.Kind}
}

func TestTheFirstDeployBehindTheALBAddsItsLoadBalancerOnceAndEveryProjectBehindItUsesIt(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction

	for _, slug := range []string{"shop", "blog", "shop"} {
		if err := b.AddParts(ctx, deployingBehindALB(slug), []string{albpart.PartName}); err != nil {
			t.Fatalf("addParts(%s) = %v", slug, err)
		}
	}

	if !slices.Equal(front.raised, []environment.Tier{tier}) {
		t.Errorf("the load balancer was raised for %v, want once for the tier", front.raised)
	}
	parts, err := b.ListDescribedParts(ctx, tier)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(parts, func(part provider.BootstrapPart) bool { return part.Name == albpart.PartName })
	if at < 0 {
		t.Fatalf("listDescribedParts() = %+v, want the %s part", parts, albpart.PartName)
	}
	if got := parts[at]; !got.RetiresWithLastUser || !slices.Equal(got.UsedBy, []string{"blog", "shop"}) || got.Inventory == nil || len(got.Inventory.Resources) == 0 {
		t.Errorf("the %s part = %+v, want it used by blog and shop, priced, and retiring with its last user", albpart.PartName, got)
	}
}

func frontingALB(t *testing.T) (bootstrap.Engine, *countingFront) {
	t.Helper()
	b, _ := servingNetworks(t)
	b.Records = fake.NewKeyValues()
	return b, b.Edges.(*frontRegistry).front
}

func TestTheLoadBalancerIsTakenDownWithTheLastProjectBehindItAndNotBefore(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	for _, slug := range []string{"shop", "blog"} {
		if err := b.AddParts(ctx, deployingBehindALB(slug), []string{albpart.PartName}); err != nil {
			t.Fatal(err)
		}
	}

	if err := albpart.Release(ctx, b, tier, "shop", true, nil); err != nil {
		t.Fatalf("releaseALB(shop) = %v", err)
	}
	if len(front.torn) != 0 {
		t.Fatalf("the load balancer was taken down while blog still deploys behind it")
	}
	if err := albpart.Release(ctx, b, tier, "blog", true, nil); err != nil {
		t.Fatalf("releaseALB(blog) = %v", err)
	}
	if !slices.Equal(front.torn, []environment.Tier{tier}) {
		t.Errorf("the load balancer was taken down for %v, want once its last project left", front.torn)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, albpart.PartName); err != nil || present {
		t.Errorf("the %s part record outlived its load balancer: %t, %v", albpart.PartName, present, err)
	}

	if err := b.AddParts(ctx, deployingBehindALB("docs"), []string{albpart.PartName}); err != nil {
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
	if err := b.AddParts(ctx, deployingBehindALB("shop"), []string{albpart.PartName}); err != nil {
		t.Fatal(err)
	}
	front.bound = []string{"shop.example.com"}

	if err := albpart.Release(ctx, b, tier, "shop", false, nil); err != nil {
		t.Fatalf("releaseALB() by a deploy moving off the alb = %v", err)
	}
	if users, _ := albpart.ListUsers(ctx, b, tier); len(front.torn) != 0 || !slices.Equal(users, []string{"shop"}) {
		t.Errorf("a deploy moving off the alb while its hostname is bound tore down %v and left users %v, want the load balancer kept and shop still its user", front.torn, users)
	}
	if err := albpart.Release(ctx, b, tier, "shop", true, nil); err != nil {
		t.Fatalf("releaseALB() by a destroy = %v", err)
	}
	if users, _ := albpart.ListUsers(ctx, b, tier); len(front.torn) != 0 || len(users) != 0 {
		t.Errorf("a destroy while a hostname is bound tore down %v and left users %v, want the load balancer kept and the project forgotten", front.torn, users)
	}
}

func TestADeployBehindTheALBWhileItIsBeingTakenDownIsRefusedAsBusy(t *testing.T) {
	t.Parallel()
	b, _ := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if _, err := albpart.WriteLease(ctx, b.Records, records.Entry{Key: albpart.LeaseAt(tier)}, albpart.Lease{Retiring: true, RetiringSince: time.Now()}); err != nil {
		t.Fatal(err)
	}

	err := b.AddParts(ctx, deployingBehindALB("shop"), []string{albpart.PartName})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Errorf("addParts() while the load balancer goes down = %v, want a %s refusal", err, refusal.CodeBusy)
	}
	if users, _ := albpart.ListUsers(ctx, b, tier); len(users) != 0 {
		t.Errorf("a refused claim left users %v, want none", users)
	}
}

func TestADryDeployBehindTheALBPlansItsLoadBalancerAndRaisesNothing(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	planned, err := b.PlanParts(context.Background(), deployingBehindALB("shop").Deploy.Tier, []string{albpart.PartName})
	if err != nil {
		t.Fatalf("PlanParts() = %v", err)
	}
	if len(front.raised) != 0 || len(planned) != 1 || planned[0].Part != albpart.PartName || planned[0].Action != provider.ActionCreate {
		t.Errorf("a dry deploy raised %v and planned %+v, want nothing raised and the %s part planned", front.raised, planned, albpart.PartName)
	}
}

func TestADeployMayAddAndRetireTheALBsGateRepositoryAndTheBaseKeepsTheCDNPurgeRole(t *testing.T) {
	t.Parallel()
	names := names.New("ocel", "acme-prod")
	deploy := deployOf(environment.TierProduction)
	if _, holds := grantedOver(t, deploy, checkedResource{name: "projects/acme-prod"})["artifactregistry.repositories.create"]; !holds {
		t.Error("a deploy lacks artifactregistry.repositories.create on the untagged project: a deploy adds the gate's repository with the load balancer")
	}
	if lines, want := credential.Lines(deploy.Grants()), deploy.Tiered(credential.RepositoriesRole).String(); !slices.Contains(lines, want) {
		t.Errorf("a deploy is granted\n%s\nwant %s: the last destroy removes the gate's repository, which carries its tier's tag", strings.Join(lines, "\n"), want)
	}
	role := bootstrap.Resource{Kind: bootstrap.KindRole, Name: names.CDNPurgeRole()}
	if !slices.Contains(googletest.IdsOf(bootstrap.Resources(names, environment.TierProduction, false)), role.ID()) {
		t.Errorf("the base provisions %v, want %s: a deploy grants Next apps behind the alb a role only a bootstrap may make", googletest.IdsOf(bootstrap.Resources(names, environment.TierProduction, false)), role.ID())
	}
	if slices.ContainsFunc(composed(bootstrap.Engine{}).Parts(), func(f provider.Part) bool { return strings.Contains(f.Name, "alb-edge") }) {
		t.Errorf("Parts() = %v, want no alb-edge part: the first deploy behind the alb adds its load balancer", composed(bootstrap.Engine{}).Parts())
	}
}

type listingTogether struct {
	records.Store
	under   records.Key
	parties int

	mu      sync.Mutex
	arrived int
	all     chan struct{}
}

func (s *listingTogether) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
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
		if err := b.AddParts(ctx, deployingBehindALB(slug), []string{albpart.PartName}); err != nil {
			t.Fatal(err)
		}
	}
	b.Records = &listingTogether{Store: b.Records, under: albpart.Consumers(tier), parties: 2, all: make(chan struct{})}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, slug := range []string{"shop", "blog"} {
		wg.Go(func() { errs[i] = albpart.Release(ctx, b, tier, slug, true, nil) })
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("releaseALB() = %v", err)
	}
	if users, _ := albpart.ListUsers(ctx, b, tier); !slices.Equal(front.torn, []environment.Tier{tier}) || len(users) != 0 {
		t.Errorf("two last projects leaving at once tore down %v and left users %v, want the load balancer taken down once", front.torn, users)
	}
}

func TestALoadBalancerWhoseTeardownFailedIsRaisedAgainByTheNextDeployBehindIt(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if err := b.AddParts(ctx, deployingBehindALB("shop"), []string{albpart.PartName}); err != nil {
		t.Fatal(err)
	}
	front.refusal = errors.New("google refused to delete the url map")

	if err := albpart.Release(ctx, b, tier, "shop", true, nil); err == nil {
		t.Fatal("releaseALB() with a failing teardown = nil, want the failure")
	}
	front.refusal = nil
	if err := b.AddParts(ctx, deployingBehindALB("docs"), []string{albpart.PartName}); err != nil {
		t.Fatalf("addParts(docs) after a failed teardown = %v", err)
	}
	if len(front.raised) != 2 {
		t.Errorf("the load balancer was raised %d times, want it raised again by the deploy after the failed teardown", len(front.raised))
	}
}

func failingALBTeardown(t *testing.T) (bootstrap.Engine, *countingFront) {
	t.Helper()
	b, front := frontingALB(t)
	ctx := context.Background()
	if err := b.AddParts(ctx, deployingBehindALB("shop"), []string{albpart.PartName}); err != nil {
		t.Fatal(err)
	}
	front.refusal = errors.New("google refused to delete the url map")
	if err := releaseALB(ctx, b, environment.TierProduction, "shop", nil); err == nil {
		t.Fatal("releaseALB() with a failing teardown = nil, want the failure")
	}
	front.refusal = nil
	return b, front
}

func TestALoadBalancerWhoseTeardownFailedStaysListedAsBehind(t *testing.T) {
	t.Parallel()
	b, _ := failingALBTeardown(t)

	parts, err := b.ListDescribedParts(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(parts, func(part provider.BootstrapPart) bool { return part.Name == albpart.PartName })
	if at < 0 {
		t.Fatalf("listDescribedParts() after a failed teardown = %+v, want the %s part still listed", parts, albpart.PartName)
	}
	if got := parts[at]; got.Current || len(got.Resources) == 0 {
		t.Errorf("the %s part after a failed teardown = %+v, want it behind and naming its resources", albpart.PartName, got)
	}
}

func TestDestroyingTheBootstrapTakesDownALoadBalancerWhoseTeardownFailed(t *testing.T) {
	t.Parallel()
	b, front := failingALBTeardown(t)
	ctx := context.Background()
	tier := environment.TierProduction

	if err := b.RemoveParts(ctx, tier, nil); err != nil {
		t.Fatalf("removeParts() = %v", err)
	}
	if !slices.Equal(front.torn, []environment.Tier{tier}) {
		t.Errorf("destroying the bootstrap tore down %v, want the load balancer whose teardown failed taken down", front.torn)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, albpart.PartName); err != nil || present {
		t.Errorf("the %s part record outlived its load balancer: %t, %v", albpart.PartName, present, err)
	}
}

func TestRemovingTheALBPartTakesDownALoadBalancerWhoseTeardownFailed(t *testing.T) {
	t.Parallel()
	b, front := failingALBTeardown(t)
	ctx := context.Background()
	tier := environment.TierProduction

	if err := b.RemoveNamedParts(ctx, tier, []string{albpart.PartName}, false, nil, nil); err != nil {
		t.Fatalf("removeNamedParts(%s) = %v", albpart.PartName, err)
	}
	if !slices.Equal(front.torn, []environment.Tier{tier}) {
		t.Errorf("--remove %s tore down %v, want the load balancer whose teardown failed taken down", albpart.PartName, front.torn)
	}
}

func TestRemovingTheLastProjectAgainRetriesALoadBalancerTeardownThatFailed(t *testing.T) {
	t.Parallel()
	b, front := failingALBTeardown(t)
	ctx := context.Background()
	tier := environment.TierProduction

	if err := releaseALB(ctx, b, tier, "shop", nil); err != nil {
		t.Fatalf("releaseALB() again = %v", err)
	}
	if users, _ := albpart.ListUsers(ctx, b, tier); !slices.Equal(front.torn, []environment.Tier{tier}) || len(users) != 0 {
		t.Errorf("removing shop again tore down %v and left users %v, want the teardown retried and shop forgotten", front.torn, users)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, albpart.PartName); err != nil || present {
		t.Errorf("the %s part record outlived its load balancer: %t, %v", albpart.PartName, present, err)
	}
}

func TestARetireCancelledMidwayLeavesTheNextDeployBehindTheALBUnrefused(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	tier := environment.TierProduction
	if err := b.AddParts(context.Background(), deployingBehindALB("shop"), []string{albpart.PartName}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	front.tearing = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}

	if err := albpart.Release(ctx, b, tier, "shop", true, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("releaseALB() cancelled during the teardown = %v, want the cancellation", err)
	}
	front.tearing = nil
	if err := b.AddParts(context.Background(), deployingBehindALB("docs"), []string{albpart.PartName}); err != nil {
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
	stale := albpart.Lease{Retiring: true, RetiringSince: time.Now().Add(-2 * albpart.RetireLimit)}
	if _, err := albpart.WriteLease(ctx, b.Records, records.Entry{Key: albpart.LeaseAt(environment.TierProduction)}, stale); err != nil {
		t.Fatal(err)
	}

	if err := b.AddParts(ctx, deployingBehindALB("shop"), []string{albpart.PartName}); err != nil {
		t.Fatalf("addParts() with a retire older than %s = %v, want the deploy to take it over", albpart.RetireLimit, err)
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
	if err := b.AddParts(ctx, deployingBehindALB("shop"), []string{albpart.PartName}); err != nil {
		t.Fatal(err)
	}
	leased, _, err := albpart.ReadLease(ctx, b.Records, tier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := albpart.WriteLease(ctx, b.Records, leased, albpart.Lease{Retiring: true, RetiringSince: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := albpart.Claim(ctx, b, tier, "shop"); refusalCode(err) != refusal.CodeBusy {
		t.Fatalf("claimALB() while the load balancer goes down = %v, want a %s refusal", err, refusal.CodeBusy)
	}
	if users, _ := albpart.ListUsers(ctx, b, tier); !slices.Equal(users, []string{"shop"}) {
		t.Errorf("a refused claim left users %v, want shop, whose record an earlier deploy wrote", users)
	}
}

func TestRemovingAProjectWhoseOnlyALBDeployFailedTakesItsLoadBalancerDown(t *testing.T) {
	t.Parallel()
	b, front := frontingALB(t)
	ctx := context.Background()
	tier := environment.TierProduction
	if err := b.AddParts(ctx, deployingBehindALB("shop"), []string{albpart.PartName}); err != nil {
		t.Fatal(err)
	}

	if err := releaseALB(ctx, b, tier, "shop", nil); err != nil {
		t.Fatalf("releaseALB() = %v", err)
	}
	if users, _ := albpart.ListUsers(ctx, b, tier); !slices.Equal(front.torn, []environment.Tier{tier}) || len(users) != 0 {
		t.Errorf("removing shop tore down %v and left users %v, want the load balancer shop alone used taken down", front.torn, users)
	}
}
