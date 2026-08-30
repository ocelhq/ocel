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

const (
	liveHostname  = "shop.example.invalid"
	claimHostname = "claimed.example.invalid"
)

func (vm machine) state(t *testing.T, container string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t,
		"sudo docker inspect -f '{{.State.Status}}' "+quote(container)+" 2>/dev/null || echo gone"))
}

type front struct {
	edge     edge.Edge
	stack    edge.EdgeStack
	hostname string
}

func fronting(t *testing.T, p *vps.Provider, slug string) front {
	t.Helper()

	opened, err := p.Edges().Open(boxedge.Kind)
	if err != nil {
		t.Fatalf("Open(%q) = %v", boxedge.Kind, err)
	}
	stack, err := opened.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassProduction, Slug: slug,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	hostname := slug + ".example.invalid"
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain(%s): %v", hostname, err)
	}
	return front{edge: opened, stack: stack, hostname: hostname}
}

func (f front) serves(t *testing.T, vm machine, path string) string {
	t.Helper()
	return strings.TrimSpace(vm.peers(t, "curl -sS -m 10 -H "+quote("Host: "+f.hostname)+
		" http://"+host.ProxyContainer+path))
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

func TestLiveARetiredContainerIsStoppedRatherThanRemovedAndARollbackRunsItAgain(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	f := fronting(t, p, "rollback")
	stack := f.stack

	one := standsUp(t, p, "one")
	promotes(t, stack, "p-one", "one", one, 1)
	if served := f.serves(t, vm, "/"); served != "one" {
		t.Fatalf("the proxy served %q after the first promotion, want the release it was pointed at", served)
	}

	two := standsUp(t, p, "two")
	promotes(t, stack, "p-two", "two", two, 2)
	if served := f.serves(t, vm, "/"); served != "two" {
		t.Fatalf("the proxy served %q after the second promotion, want the release it was pointed at", served)
	}
	if state := vm.state(t, one.physical); state != "exited" {
		t.Fatalf("the retired container reads as %q, want it stopped and standing: this release loop stops what it retires and never removes it, so the release it rolled off can still be read for logs and an exit code after the flip", state)
	}

	promotes(t, stack, "p-rollback", "one", one, 3)

	if state := vm.state(t, one.physical); state != "running" {
		t.Errorf("the container the rollback re-points at reads as %q, want it running: nothing provisions on this path, so a promote that does not make the containers running is a ledger edit and not a restored site. The rollback runs the image again under that name rather than starting the container that stood there", state)
	}
	if served := f.serves(t, vm, "/"); served != "one" {
		t.Errorf("the proxy served %q after the rollback, want the release it was rolled back onto", served)
	}
	if state := vm.state(t, two.physical); state != "exited" {
		t.Errorf("the container the rollback rolled off reads as %q, want it stopped and standing", state)
	}
	if held := windowOf(t, vm, liveApp); len(held) == 0 || held[0] != fixtureAt("one") {
		t.Errorf("the box's release window reads %v, want %s at its head: rolling back is what this box most recently served, and a window the rollback does not re-head has the release it restored swept off by the next deploy's reconcile while the ledger still offers it", held, fixtureAt("one"))
	}
}

func TestLiveARollbackRunsTheSameImageDigestTheBoxAlreadyHeld(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	f := fronting(t, p, "retained")
	stack := f.stack

	one := standsUp(t, p, "one")
	promotes(t, stack, "p-one", "one", one, 1)
	held := vm.holds(t, fixtureAt("one"))

	two := standsUp(t, p, "two")
	promotes(t, stack, "p-two", "two", two, 2)

	promotes(t, stack, "p-rollback", "one", one, 3)

	if again := vm.inspects(t, "image", fixtureAt("one"), "{{.Id}}"); again != held {
		t.Errorf("the image the rollback ran is %q, want the %q this box already held: a rollback re-points at a retained digest, and a coordinate that resolves to a different image is one this box rebuilt or fetched behind the rollback", again, held)
	}
	if served := f.serves(t, vm, "/"); served != "one" {
		t.Errorf("the proxy served %q after the rollback, want the release it was rolled back onto", served)
	}
}

func TestLiveAClaimedHostnameIsLoadedOntoTheProxyAndChangesNothingItServes(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	f := fronting(t, p, "domains")
	stack := f.stack
	ctx := context.Background()

	one := standsUp(t, p, "one")
	promotes(t, stack, "p-one", "one", one, 1)

	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: claimHostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	t.Cleanup(func() {
		if err := stack.UnbindDomain(context.Background(), claimHostname); err != nil {
			t.Errorf("UnbindDomain: %v", err)
		}
	})

	owner, err := f.edge.DomainOwner(ctx, claimHostname)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if want := boxedge.Surface("domains", edge.ClassProduction); owner != want {
		t.Errorf("DomainOwner(%q) = %q, want %q read back off the configuration the running proxy was given", claimHostname, owner, want)
	}

	claimed := strings.TrimSpace(vm.peers(t, "curl -sS -m 10 -H "+quote("Host: "+claimHostname)+" http://"+host.ProxyContainer+"/"))
	if claimed != "one" {
		t.Errorf("the proxy answered %q for the claimed hostname, want the release it serves: claiming a hostname records which project answers it and is not itself what answers it", claimed)
	}
	if served := f.serves(t, vm, "/"); served != "one" {
		t.Errorf("the proxy served %q on the hostname it was already answering, want the release it served before the second claim: binding another name adds one, and a claim that moves what the names already bound answer breaks a site to add a domain to it", served)
	}
	refused := vm.peers(t, "curl -sS -m 10 -o /dev/null -D - -H "+quote("Host: unclaimed.example.invalid")+" http://"+host.ProxyContainer+"/")
	if !strings.Contains(refused, "404") || !strings.Contains(strings.ToLower(refused), strings.ToLower(host.EdgeHeader)+": "+host.EdgeName) {
		t.Errorf("a hostname nothing on this box claims was answered with\n%s\nwant a bare 404 carrying %s: %s, because an empty 200 reads as healthy to everything that checks it", refused, host.EdgeHeader, host.EdgeName)
	}

	unclaimed, err := f.edge.DomainOwner(ctx, "unclaimed.example.invalid")
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if unclaimed != "" {
		t.Errorf("DomainOwner(unclaimed) = %q, want nothing claiming a hostname nothing was bound to", unclaimed)
	}
}

var liveSlug atomic.Int64

func TestLiveTheBoxEdgeAnswersTheEdgeContractsLedgerAndDomainObligationsAgainstARealMachine(t *testing.T) {
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

func TestLiveARollbackOntoAnImageTheBoxHasSweptIsRefusedAndLeavesTheSiteServing(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	f := fronting(t, p, "swept")
	stack := f.stack

	one := standsUp(t, p, "one")
	promotes(t, stack, "p-one", "one", one, 1)
	two := standsUp(t, p, "two")
	promotes(t, stack, "p-two", "two", two, 2)

	vm.ssh(t, "sudo docker rm --force "+quote(one.physical)+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo docker rmi "+quote(fixtureAt("one")))

	err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p-rollback", Ts: 3, Builds: map[string]string{liveApp: "one"},
	}, "", edge.DiscardReporter())
	if err == nil {
		t.Fatal("a rollback onto an image this box no longer holds succeeded, and docker run would then reach for a registry with no credentials on this path")
	}
	if !strings.Contains(err.Error(), "deploy again") {
		t.Errorf("the refusal reads %q and never says what to do instead", err)
	}
	if served := f.serves(t, vm, "/"); served != "two" {
		t.Errorf("the proxy served %q after a refused rollback, want the release that was serving before it: the ensure runs before the flip, so a rollback that cannot serve moves nothing", served)
	}
}
