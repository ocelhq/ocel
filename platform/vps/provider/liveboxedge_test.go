package vps_test

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/edgeconformance"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const liveHostname = "shop.example.invalid"

func (vm machine) state(t *testing.T, container string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t,
		"sudo docker inspect -f '{{.State.Status}}' "+quote(container)+" 2>/dev/null || echo gone"))
}

func fronting(t *testing.T, p *vps.Provider, slug string) (edge.Edge, edge.EdgeStack) {
	t.Helper()

	front, err := p.Edges().Open(boxedge.Kind)
	if err != nil {
		t.Fatalf("Open(%q) = %v", boxedge.Kind, err)
	}
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassProduction, Slug: slug,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return front, stack
}

func promotes(t *testing.T, stack edge.EdgeStack, id, tag string, held release, at int64) {
	t.Helper()

	ctx := context.Background()
	if err := stack.Ledger().PutStaged(ctx, edge.DeploymentRecord{
		App:        liveApp,
		Identity:   tag,
		Entry:      "/",
		Image:      fixtureAt(tag),
		Physical:   held.physical,
		HealthPath: healthPath,
	}); err != nil {
		t.Fatalf("PutStaged(%s): %v", tag, err)
	}
	if err := stack.Promote(ctx, edge.Promotion{
		PromotionID: id, Ts: at, Builds: map[string]string{liveApp: tag},
	}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(%s): %v", id, err)
	}
}

func TestLiveARetiredContainerIsStoppedRatherThanRemovedAndARollbackStartsItAgain(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	_, stack := fronting(t, p, "rollback")

	one := standsUp(t, p, "one")
	promotes(t, stack, "p-one", "one", one, 1)
	if served := servedBy(t, vm, "/"); served != "one" {
		t.Fatalf("the proxy served %q after the first promotion, want the release it was pointed at", served)
	}

	two := standsUp(t, p, "two")
	promotes(t, stack, "p-two", "two", two, 2)
	if served := servedBy(t, vm, "/"); served != "two" {
		t.Fatalf("the proxy served %q after the second promotion, want the release it was pointed at", served)
	}
	if state := vm.state(t, one.physical); state != "exited" {
		t.Fatalf("the retired container reads as %q, want it stopped and standing: a rollback the ledger still offers has nothing to start again once its container has been removed", state)
	}

	promotes(t, stack, "p-rollback", "one", one, 3)

	if state := vm.state(t, one.physical); state != "running" {
		t.Errorf("the container the rollback re-points at reads as %q, want it running: nothing provisions on this path, so a promote that does not make the containers running is a ledger edit and not a restored site", state)
	}
	if served := servedBy(t, vm, "/"); served != "one" {
		t.Errorf("the proxy served %q after the rollback, want the release it was rolled back onto", served)
	}
	if state := vm.state(t, two.physical); state != "exited" {
		t.Errorf("the container the rollback rolled off reads as %q, want it stopped and standing", state)
	}
}

func TestLiveARollbackRunsTheImageTheBoxAlreadyHoldsAndPullsNothing(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	_, stack := fronting(t, p, "retained")

	one := standsUp(t, p, "one")
	promotes(t, stack, "p-one", "one", one, 1)
	held := vm.holds(t, fixtureAt("one"))

	two := standsUp(t, p, "two")
	promotes(t, stack, "p-two", "two", two, 2)

	promotes(t, stack, "p-rollback", "one", one, 3)

	if again := vm.inspects(t, "image", fixtureAt("one"), "{{.Id}}"); again != held {
		t.Errorf("the image the rollback ran is %q, want the %q this box already held: a rollback re-points at a retained digest with no rebuild and no re-transfer", again, held)
	}
	if served := servedBy(t, vm, "/"); served != "one" {
		t.Errorf("the proxy served %q after the rollback, want the release it was rolled back onto", served)
	}
}

func TestLiveAClaimedHostnameIsLoadedOntoTheProxyAndChangesNothingItServes(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	front, stack := fronting(t, p, "domains")
	ctx := context.Background()

	one := standsUp(t, p, "one")
	promotes(t, stack, "p-one", "one", one, 1)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: liveHostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	t.Cleanup(func() {
		if err := stack.UnbindDomain(context.Background(), liveHostname); err != nil {
			t.Errorf("UnbindDomain: %v", err)
		}
	})

	owner, err := front.DomainOwner(ctx, liveHostname)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if want := boxedge.Surface("domains", edge.ClassProduction); owner != want {
		t.Errorf("DomainOwner(%q) = %q, want %q read back off the configuration the running proxy was given", liveHostname, owner, want)
	}

	claimed := strings.TrimSpace(vm.peers(t, "curl -sS -m 10 -H "+quote("Host: "+liveHostname)+" http://"+host.ProxyContainer+"/"))
	if claimed != "one" {
		t.Errorf("the proxy answered %q for the claimed hostname, want the release it serves: claiming a hostname records which project answers it and is not itself what answers it", claimed)
	}
	if served := servedBy(t, vm, "/"); served != "one" {
		t.Errorf("the proxy served %q for every other hostname after the claim, want the release it served before it", served)
	}

	unclaimed, err := front.DomainOwner(ctx, "unclaimed.example.invalid")
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if unclaimed != "" {
		t.Errorf("DomainOwner(unclaimed) = %q, want nothing claiming a hostname nothing was bound to", unclaimed)
	}
}

var liveSlug atomic.Int64

func TestLiveTheBoxEdgeAnswersTheEdgeContractAgainstARealMachine(t *testing.T) {
	vm := live(t)
	bootstrapped(t, vm, providerkit.ClassProduction)
	p := vm.deploying(t)
	defer closing(t, p)

	edgeconformance.Run(t, edgeconformance.Suite{
		Hostname: liveHostname,
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			front, err := p.Edges().Open(boxedge.Kind)
			if err != nil {
				t.Fatalf("Open(%q) = %v", boxedge.Kind, err)
			}
			return front, edge.StackSpec{
				Version: "test",
				Class:   edge.ClassProduction,
				Slug:    "conformance" + strconv.FormatInt(liveSlug.Add(1), 10),
			}
		},
	})
}
