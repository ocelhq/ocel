package vps_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/router"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
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
	routes   router.Stack
	hostname string
}

func fronting(t *testing.T, p *vps.Provider, slug string) front {
	t.Helper()

	opened, err := p.Edges().Open(edge.None)
	if err != nil {
		t.Fatalf("Open(%q) = %v", edge.None, err)
	}
	stack, err := opened.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Tier: environment.TierProduction, Slug: slug,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	hostname := slug + ".example.invalid"
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain(%s): %v", hostname, err)
	}
	return front{edge: opened, stack: stack, routes: routed(t, p, stack), hostname: hostname}
}

func (f front) serves(t *testing.T, vm machine, path string) string {
	t.Helper()
	return strings.TrimSpace(vm.peers(t, "curl -sS -m 10 -H "+quote("Host: "+f.hostname)+
		" http://"+caddy.Container+path))
}

func liveRecord(tag string, staged release) router.DeploymentRecord {
	return router.DeploymentRecord{
		App:        liveApp,
		Build:      tag,
		Entry:      "/",
		Image:      fixtureAt(tag),
		Physical:   staged.physical,
		HealthPath: healthPath,
	}
}

func promotes(t *testing.T, p *vps.Provider, stack edge.EdgeStack, id, tag string, staged release, at int64) {
	t.Helper()
	promotion := router.Promotion{PromotionID: id, Ts: at, Builds: map[string]string{liveApp: tag}}
	promotesRecord(t, p, stack, "", promotion, liveRecord(tag, staged))
}

var (
	liveRun        = strconv.FormatInt(time.Now().UnixNano(), 36)
	livePromotions atomic.Int64
)

func ownPromotion(promotion router.Promotion) router.Promotion {
	promotion.PromotionID = fmt.Sprintf("%s-%s-%d", promotion.PromotionID, liveRun, livePromotions.Add(1))
	return promotion
}

func promotesRecord(t *testing.T, p *vps.Provider, stack edge.EdgeStack, pointer string, promotion router.Promotion, record router.DeploymentRecord) {
	t.Helper()

	ctx := context.Background()
	promotion = ownPromotion(promotion)
	state := stack.State()
	releases := ledger.New(p.KeyValues(), state.Tier, state.Slug)
	if err := releases.PutStaged(ctx, record); err != nil {
		t.Fatalf("PutStaged(%s/%s): %v", record.App, record.Build, err)
	}
	replaces, err := releases.ActivePromotionID(ctx, pointer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releases.Promote(ctx, promotion, pointer, replaces); err != nil {
		t.Fatalf("Promote(%s): %v", promotion.PromotionID, err)
	}
	if err := routed(t, p, stack).Flip(ctx, router.Flip{
		Pointer:   pointer,
		Promotion: promotion,
		Records:   map[string]router.DeploymentRecord{record.App: record},
	}, progress.Discard()); err != nil {
		t.Fatalf("Flip(%s): %v", promotion.PromotionID, err)
	}
}

func TestLiveARetiredContainerIsStoppedRatherThanRemovedAndARollbackRunsItAgain(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	f := fronting(t, p, "rollback")

	one := provisioned(t, p, "one")
	promotes(t, p, f.stack, "p-one", "one", one, 1)
	if served := f.serves(t, vm, "/"); served != "one" {
		t.Fatalf("the proxy served %q after the first promotion, want the release it was pointed at", served)
	}

	two := provisioned(t, p, "two")
	promotes(t, p, f.stack, "p-two", "two", two, 2)
	if served := f.serves(t, vm, "/"); served != "two" {
		t.Fatalf("the proxy served %q after the second promotion, want the release it was pointed at", served)
	}
	if state := vm.state(t, one.physical); state != "exited" {
		t.Fatalf("the retired container reads as %q, want it stopped and still present: this release loop stops what it retires and never removes it, so the release it rolled off can still be read for logs and an exit code after the flip", state)
	}

	promotes(t, p, f.stack, "p-rollback", "one", one, 3)

	if state := vm.state(t, one.physical); state != "running" {
		t.Errorf("the container the rollback re-points at reads as %q, want it running: nothing provisions on this path, so a promote that does not make the containers running is a ledger edit and not a restored site. The rollback runs the image again under that name rather than starting the container that was there", state)
	}
	if served := f.serves(t, vm, "/"); served != "one" {
		t.Errorf("the proxy served %q after the rollback, want the release it was rolled back onto", served)
	}
	if state := vm.state(t, two.physical); state != "exited" {
		t.Errorf("the container the rollback rolled off reads as %q, want it stopped and still present", state)
	}
	if window := windowOf(t, vm, "rollback", liveApp, environment.TierProduction); len(window) == 0 || window[0] != fixtureAt("one") {
		t.Errorf("the box's release window reads %v, want %s at its head: rolling back is what this box most recently served, and a window the rollback does not re-head has the release it restored swept off by the next deploy's reconcile while the ledger still offers it", window, fixtureAt("one"))
	}
}

func TestLiveARollbackRunsTheSameImageDigestTheBoxAlreadyHad(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	f := fronting(t, p, "retained")

	one := provisioned(t, p, "one")
	promotes(t, p, f.stack, "p-one", "one", one, 1)
	retained := vm.imageID(t, fixtureAt("one"))

	two := provisioned(t, p, "two")
	promotes(t, p, f.stack, "p-two", "two", two, 2)

	promotes(t, p, f.stack, "p-rollback", "one", one, 3)

	if again := vm.inspects(t, "image", fixtureAt("one"), "{{.Id}}"); again != retained {
		t.Errorf("the image the rollback ran is %q, want the %q this box already retained: a rollback re-points at a retained digest, and a coordinate that resolves to a different image is one this box rebuilt or fetched behind the rollback", again, retained)
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

	one := provisioned(t, p, "one")
	promotes(t, p, f.stack, "p-one", "one", one, 1)

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
	if want := boxedge.Surface("domains", environment.TierProduction); owner != want {
		t.Errorf("DomainOwner(%q) = %q, want %q read back off the configuration the running proxy was given", claimHostname, owner, want)
	}

	claimed := strings.TrimSpace(vm.peers(t, "curl -sS -m 10 -H "+quote("Host: "+claimHostname)+" http://"+caddy.Container+"/"))
	if claimed != "one" {
		t.Errorf("the proxy answered %q for the claimed hostname, want the release it serves: claiming a hostname records which project answers it and is not itself what answers it", claimed)
	}
	if served := f.serves(t, vm, "/"); served != "one" {
		t.Errorf("the proxy served %q on the hostname it was already answering, want the release it served before the second claim: binding another name adds one, and a claim that moves what the names already bound answer breaks a site to add a domain to it", served)
	}
	refused := vm.peers(t, "curl -sS -m 10 -o /dev/null -D - -H "+quote("Host: unclaimed.example.invalid")+" http://"+caddy.Container+"/")
	if !strings.Contains(refused, "404") || !strings.Contains(strings.ToLower(refused), strings.ToLower(router.HeaderRouter)+": "+string(switchboard.RouterKind)) {
		t.Errorf("a hostname nothing on this box claims was answered with\n%s\nwant a bare 404 with %s: %s, because an empty 200 reads as healthy to everything that checks it", refused, router.HeaderRouter, switchboard.RouterKind)
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
	vm := liveMachine(t)
	bootstrapped(t, vm, environment.TierProduction)
	p := vm.deploying(t)
	defer closing(t, p)

	edgeconformance.Run(t, edgeconformance.Suite{
		Hostname: liveHostname,
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			front, err := p.Edges().Open(edge.None)
			if err != nil {
				t.Fatalf("Open(%q) = %v", edge.None, err)
			}
			return front, edge.StackSpec{
				Version: "test",
				Tier:    environment.TierProduction,
				Slug:    "conformance" + strconv.FormatInt(liveSlug.Add(1), 10),
			}
		},
	})
}

func TestLiveARollbackOntoAnImageTheBoxHasSweptIsRefusedAndLeavesTheSiteServing(t *testing.T) {
	vm, p := onABoxServingContainers(t)
	defer closing(t, p)

	f := fronting(t, p, "swept")

	one := provisioned(t, p, "one")
	promotes(t, p, f.stack, "p-one", "one", one, 1)
	two := provisioned(t, p, "two")
	promotes(t, p, f.stack, "p-two", "two", two, 2)

	vm.ssh(t, "sudo docker rm --force "+quote(one.physical)+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo docker rmi "+quote(fixtureAt("one")))

	err := f.routes.Flip(context.Background(), router.Flip{
		Promotion: router.Promotion{PromotionID: "p-rollback", Ts: 3, Builds: map[string]string{liveApp: "one"}},
		Records:   map[string]router.DeploymentRecord{liveApp: liveRecord("one", one)},
	}, progress.Discard())
	if err == nil {
		t.Fatal("a rollback onto an image this box no longer has succeeded, and docker run would then reach for a registry with no credentials on this path")
	}
	if !strings.Contains(err.Error(), "Deploy again") {
		t.Errorf("the refusal reads %q and never says what to do instead", err)
	}
	if served := f.serves(t, vm, "/"); served != "two" {
		t.Errorf("the proxy served %q after a refused rollback, want the release that was serving before it: the ensure runs before the flip, so a rollback that cannot serve moves nothing", served)
	}
}
