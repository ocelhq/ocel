package alb

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestAProtectedProductionHostIsInTheProductionPluginAndProvenGatedBeforeItIsRouted(t *testing.T) {
	t.Parallel()

	balancer, w := gatedEdgeOn(t, environment.TierProduction)
	shop := reconcileGatedOn(t, balancer, environment.TierProduction, "shop", &productionGate)
	bindShop(t, shop)

	stage(t, shop, staticRecord("b1"))
	promote(t, shop, "p1", "b1")

	if hosts := w.gatedHosts(GateStack(environment.TierProduction)); !slices.Equal(hosts, []string{"shop.example.com"}) {
		t.Errorf("the production plugin gates %v, want shop.example.com", hosts)
	}
	events := w.happened()
	probedAt, routedAt := indexOf(events, "probe shop.example.com"), indexOf(events, "route shop.example.com")
	if probedAt < 0 || routedAt < 0 || probedAt > routedAt {
		t.Errorf("events %v, want shop.example.com proven gated before it was routed: a host the gate does not reach would serve production to anyone", events)
	}
}

func TestAProtectedProductionIsWarmedWithASessionItsOwnGateAdmits(t *testing.T) {
	t.Parallel()

	balancer, w := gatedEdgeOn(t, environment.TierProduction)
	shop := reconcileGatedOn(t, balancer, environment.TierProduction, "shop", &productionGate)
	bindShop(t, shop)
	stage(t, shop, staticRecord("b1"))
	promote(t, shop, "p1", "b1")
	stage(t, shop, staticRecord("b2"))
	promote(t, shop, "p2", "b2")

	w.mu.Lock()
	cookies := slices.Clone(w.warmCookies)
	w.mu.Unlock()
	if len(cookies) == 0 {
		t.Fatal("the promotion warmed nothing")
	}
	last := cookies[len(cookies)-1]
	if verdict := productionGate.Check(gate.Request{Method: "HEAD", Host: "shop.example.com", Target: "/", Header: gate.Header{"Cookie": {last}}}, time.Now()); verdict.Response != nil {
		t.Errorf("the warm carried %q, which the production gate refuses with %d, so it warms nothing", last, verdict.Response.Status)
	}
}

func TestAProjectsProductionAndPreviewGatesNeverShareAPluginOrARecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdgeOn(t, tiers...)
	reconcileGatedOn(t, balancer, environment.TierPreview, "shop", &shopGate)
	reconcileGatedOn(t, balancer, environment.TierProduction, "shop", &productionGate)

	production, preview := w.gateConfig(GateStack(environment.TierProduction)), w.gateConfig(GateStack(environment.TierPreview))
	if !strings.Contains(production, productionGate.Key) || strings.Contains(production, shopGate.Key) {
		t.Errorf("the production plugin runs\n%s\nwant the production key alone: a preview password must never open production", production)
	}
	if !strings.Contains(preview, shopGate.Key) || strings.Contains(preview, productionGate.Key) {
		t.Errorf("the preview plugin runs\n%s\nwant the preview key alone", preview)
	}
	entry, err := records.ReadOrEmpty(ctx, balancer.deps.KeyValues, gateRecordKey(environment.TierProduction, "shop"))
	if err != nil || len(entry.Value) == 0 {
		t.Fatalf("the production gate record = %v, %v, want it kept", entry, err)
	}
	var opened gateRecord
	if err := balancer.openRecord(ctx, environment.TierPreview, gateRecordData(environment.TierPreview, "shop"), entry.Value, &opened); err == nil {
		t.Error("the production gate record opens as the preview tier's, and one tier's records would pass for the other's")
	}
}

func TestTurningProductionProtectionOffOpensProductionAlone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdgeOn(t, tiers...)
	reconcileGatedOn(t, balancer, environment.TierPreview, "shop", &shopGate)
	reconcileGatedOn(t, balancer, environment.TierProduction, "shop", &productionGate)

	reconcileGatedOn(t, balancer, environment.TierProduction, "shop", nil)

	if config := w.gateConfig(GateStack(environment.TierProduction)); strings.Contains(config, productionGate.Key) {
		t.Errorf("the production plugin still runs shop's gate after protection.production was turned off:\n%s", config)
	}
	if config := w.gateConfig(GateStack(environment.TierPreview)); !strings.Contains(config, shopGate.Key) {
		t.Errorf("the preview plugin lost shop's gate when production was opened:\n%s", config)
	}
	if _, found, err := balancer.readGateRecord(ctx, environment.TierPreview, "shop"); err != nil || !found {
		t.Errorf("the preview gate record = %t, %v, want it kept", found, err)
	}
}

func TestPublishingAProductionGateSendsItToTheProductionPluginAlone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdgeOn(t, tiers...)
	state := reconcileGatedOn(t, balancer, environment.TierProduction, "shop", nil).State()

	if err := balancer.Hooks().PublishGate(ctx, edge.GatePublication{State: state, Gate: &productionGate}); err != nil {
		t.Fatalf("PublishGate = %v", err)
	}

	if config := w.gateConfig(GateStack(environment.TierProduction)); !strings.Contains(config, productionGate.Key) {
		t.Errorf("the production plugin runs %q, want the published gate", config)
	}
	if config := w.gateConfig(GateStack(environment.TierPreview)); strings.Contains(config, productionGate.Key) {
		t.Errorf("the preview plugin runs the production gate:\n%s", config)
	}
}

func TestDestroyingAProtectedProductionProjectRemovesItsGateFromTheProductionPlugin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdgeOn(t, environment.TierProduction)
	shop := reconcileGatedOn(t, balancer, environment.TierProduction, "shop", &productionGate)

	if err := shop.Destroy(ctx); err != nil {
		t.Fatalf("Destroy = %v", err)
	}

	if config := w.gateConfig(GateStack(environment.TierProduction)); strings.Contains(config, productionGate.Key) {
		t.Errorf("the production plugin still holds the destroyed project's keys:\n%s", config)
	}
}

func recordSealedFor(t *testing.T, balancer *Edge, key records.Key, bound seal.AssociatedData, record any) {
	t.Helper()
	plaintext, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := balancer.deps.Cipher.Seal(context.Background(), environment.TierPreview, bound, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := balancer.deps.KeyValues.Write(context.Background(), records.Entry{Key: key, Value: value}); err != nil {
		t.Fatal(err)
	}
}

func withPreviewOnlyGateRecords(t *testing.T) (*Edge, *world) {
	t.Helper()
	balancer, w := balancing(t)
	partition := stackrecords.EdgeStacksPartition(environment.TierPreview)
	recordSealedFor(t, balancer, partition.Key(string(Kind), "gate-publication"),
		seal.Bind("alb preview gate publication"), gatePublication{Confirmed: true})
	recordSealedFor(t, balancer, partition.Key(string(Kind), "gates", "blog"),
		seal.Bind("alb preview gate", "blog"), gateRecord{Gate: blogGate})
	return balancer, w
}

func TestAnUnprotectedPreviewDeploysOnATierWhoseGateRecordsWereSealedForPreviewsAlone(t *testing.T) {
	t.Parallel()
	balancer, _ := withPreviewOnlyGateRecords(t)

	if _, err := balancer.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierPreview}, edge.StackState{}); err != nil {
		t.Errorf("Reconcile = %v, want an unprotected preview to need nothing of the gate records sealed before gates were per tier", err)
	}
}

func TestBootstrappingAPreviewTierWhoseGateRecordsWereSealedForPreviewsAloneAttachesTheGate(t *testing.T) {
	t.Parallel()
	balancer, w := withPreviewOnlyGateRecords(t)

	if _, err := balancer.Bootstrap(context.Background(), environment.TierPreview); err != nil {
		t.Fatalf("Bootstrap = %v, want the tier bootstrapped past the gate records sealed before gates were per tier", err)
	}
	reconcileGated(t, balancer, "shop", &shopGate)

	if config := w.gateConfig(GateStack(environment.TierPreview)); !strings.Contains(config, shopGate.Key) {
		t.Errorf("the preview plugin holds %s, want the protected preview's keys", config)
	}
}

func TestDestroyingAProjectReleasesTheLoadBalancerOnlyOnceItsHostsAreUnrouted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	var released []string
	balancer.deps.FrontPart = &FrontPart{
		Claim: func(context.Context, environment.Tier, string) error { return nil },
		Release: func(_ context.Context, tier environment.Tier, slug string) error {
			released = append(released, string(tier)+"/"+slug)
			if hosts := w.hosts(loadBalancerNames(tier, false).URLMap); len(hosts) != 0 {
				t.Errorf("the load balancer was released while it still routes %v", hosts)
			}
			return nil
		},
	}
	router := routerStack{s: reconcileGated(t, balancer, "shop", &shopGate)}
	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatal(err)
	}

	if err := router.s.Destroy(ctx); err != nil {
		t.Fatalf("Destroy = %v", err)
	}
	if !slices.Equal(released, []string{"preview/shop"}) {
		t.Errorf("Destroy released %v, want the preview load balancer released by shop: the last project behind it takes it down", released)
	}
}
