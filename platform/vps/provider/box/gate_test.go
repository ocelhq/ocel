package box_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

var (
	recordedGate   = gate.Gate{Key: "k", PasswordHash: "hash", BypassSecrets: []string{"s"}}
	productionGate = gate.Gate{Key: "p", PasswordHash: "production-hash", BypassSecrets: []string{"t"}}
)

const productionHost = "shop.example.com"

var tiers = []environment.Tier{environment.TierPreview, environment.TierProduction}

func indexOf(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(call string) bool { return strings.HasPrefix(call, prefix) })
}

func stackOfTier(t *testing.T, m *machine, tier environment.Tier) boxStack {
	t.Helper()
	if tier == environment.TierPreview {
		return previewStack(t, m)
	}
	return reconciledOn(t, m, slug)
}

func servedOn(t *testing.T, stack boxStack, tier environment.Tier) {
	t.Helper()
	if tier == environment.TierPreview {
		previewed(t, stack, "pr-7", "web")
		return
	}
	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: productionHost, App: "web"}); err != nil {
		t.Fatalf("Claim(%s) = %v", productionHost, err)
	}
	staged(t, stack, "web", "b-1", slug+"-web-1")
	if err := promoted(t, stack, "p-1", "web", "b-1"); err != nil {
		t.Fatalf("promoted() = %v", err)
	}
}

func TestABoxDeclaresItGates(t *testing.T) {
	t.Parallel()

	_, front, _ := reconciled(t)

	if !front.Facts().Gates {
		t.Error("Facts().Gates = false, want true: the switchboard on the box runs the gate, and a protected deploy is refused through an edge that says otherwise")
	}
}

func TestEachTierHandsTheSwitchboardTheGateRecordedForItBeforeAnyHostnameIsClaimedOrReleased(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			m := aMachine()
			m.record(tier, slug, recordedGate)

			servedOn(t, stackOfTier(t, m, tier), tier)

			surface := box.Surface(slug, tier)
			if got := m.gates[surface]; !slices.Equal(got.BypassSecrets, recordedGate.BypassSecrets) || got.Key != recordedGate.Key {
				t.Fatalf("the box holds gate %+v for %s, want %+v", got, surface, recordedGate)
			}
			gated, claimed, released := indexOf(m.calls, "gate "+surface), indexOf(m.calls, "claim "), indexOf(m.calls, "release ")
			if gated < 0 || claimed < 0 || released < 0 || gated > claimed || gated > released {
				t.Errorf("calls = %v, want the gate put before a hostname is claimed or a release goes live: one claimed first answers ungated until the gate lands", m.calls)
			}
		})
	}
}

func TestAMoveOfAProjectWithNoRecordedGateTakesTheGateTheBoxHeld(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			m := aMachine()
			surface := box.Surface(slug, tier)
			m.gates[surface] = recordedGate

			servedOn(t, stackOfTier(t, m, tier), tier)

			if got, held := m.gates[surface]; held {
				t.Errorf("the box still holds gate %+v for %s, want it taken once none is recorded", got, surface)
			}
		})
	}
}

func TestAPreviewMoveWhoseGateCannotBeReadClaimsNothingSoNoHostnameAnswersUngated(t *testing.T) {
	t.Parallel()

	m := aMachine()
	m.refuseOn("ReadGate", errors.New("the key-value store is unreachable"))
	stack := previewStack(t, m)
	staged(t, stack, "web", "b-pr-7", slug+"-web-pr-7")

	err := stack.MovePointer(context.Background(), router.PointerMove{Pointer: "pr-7", Hosts: listPreviewHosts("pr-7", "web"), Promotion: router.Promotion{
		PromotionID: "p-pr-7", Ts: 1, Releases: map[string]string{"web": "b-pr-7"},
	}}, progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer() = %v, want it unserved", err)
	}
	if claimed := claimedOn(t, m); len(claimed) != 0 {
		t.Errorf("the box claimed %v, want nothing", claimed)
	}
}

func TestAProductionClaimWhoseGateCannotBeReadClaimsNothingSoTheHostnameNeverAnswersUngated(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := reconciledOn(t, m, slug)
	m.refuseOn("ReadGate", errors.New("the key-value store is unreachable"))

	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: productionHost, App: "web"}); err == nil {
		t.Fatal("Claim() = nil, want the unread gate to refuse the claim")
	}
	if claimed := claimedOn(t, m); len(claimed) != 0 {
		t.Errorf("the box claimed %v, want nothing", claimed)
	}
}

func TestAProductionMoveWhoseGateCannotBeReadReleasesNothing(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := reconciledOn(t, m, slug)
	staged(t, stack, "web", "b-1", slug+"-web-1")
	m.refuseOn("ReadGate", errors.New("the key-value store is unreachable"))

	err := promoted(t, stack, "p-1", "web", "b-1")

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("promoted() = %v, want it unserved", err)
	}
	if len(m.rollouts) != 0 {
		t.Errorf("the box rolled out %v, want nothing released while its gate is unknown", m.rollouts)
	}
}

func TestEachTierIsGatedByItsOwnRecordAndNeverByTheOtherTiers(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			m := aMachine()
			other := environment.TierProduction
			if tier == environment.TierProduction {
				other = environment.TierPreview
			}
			m.record(other, slug, recordedGate)

			servedOn(t, stackOfTier(t, m, tier), tier)

			if got, held := m.gates[box.Surface(slug, tier)]; held {
				t.Errorf("the %s surface holds gate %+v, want none: only the %s tier is protected", tier, got, other)
			}
			if slices.Contains(m.gateReads, other) {
				t.Errorf("a %s move read the gates of tiers %v, want only its own", tier, m.gateReads)
			}
		})
	}
}

func TestBothTiersOfAProjectHoldTheirOwnGatesSideBySide(t *testing.T) {
	t.Parallel()

	m := aMachine()
	m.record(environment.TierPreview, slug, recordedGate)
	m.record(environment.TierProduction, slug, productionGate)
	servedOn(t, stackOfTier(t, m, environment.TierPreview), environment.TierPreview)
	servedOn(t, stackOfTier(t, m, environment.TierProduction), environment.TierProduction)

	if got := m.gates[box.Surface(slug, environment.TierPreview)]; got.Key != recordedGate.Key {
		t.Errorf("the preview surface holds gate %+v, want %+v", got, recordedGate)
	}
	if got := m.gates[box.Surface(slug, environment.TierProduction)]; got.Key != productionGate.Key {
		t.Errorf("the production surface holds gate %+v, want %+v", got, productionGate)
	}
}

func TestDestroyingAStackTakesItsGateOffTheBoxAndLeavesTheOtherTiers(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			m := aMachine()
			m.record(tier, slug, recordedGate)
			stack := stackOfTier(t, m, tier)
			servedOn(t, stack, tier)
			other := box.Surface(slug, environment.TierProduction)
			if tier == environment.TierProduction {
				other = box.Surface(slug, environment.TierPreview)
			}
			m.gates[other] = productionGate

			if err := stack.Destroy(context.Background()); err != nil {
				t.Fatalf("Destroy() = %v", err)
			}

			if got, held := m.gates[box.Surface(slug, tier)]; held {
				t.Errorf("the box holds gate %+v for the destroyed %s stack, want none", got, tier)
			}
			if _, held := m.gates[other]; !held {
				t.Errorf("destroying the %s stack took %s's gate too, want it kept", tier, other)
			}
		})
	}
}

func TestPublishingAGateHandsTheSwitchboardItWithoutAMove(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			m := aMachine()
			stack := stackOfTier(t, m, tier)
			surface := box.Surface(slug, tier)
			publish := edgeOver(m, fake.NewKeyValues()).Hooks().PublishGate
			if publish == nil {
				t.Fatal("Hooks().PublishGate = nil, want the box to hand the switchboard a gate the moment it changes")
			}

			if err := publish(context.Background(), edge.GatePublication{State: stack.State(), Gate: &recordedGate}); err != nil {
				t.Fatalf("PublishGate = %v", err)
			}

			if got, held := m.gates[surface]; !held || got.Key != recordedGate.Key {
				t.Errorf("the box holds gate %+v for %s, want %+v", got, surface, recordedGate)
			}
			if len(m.gates) != 1 {
				t.Errorf("the box holds gates %v, want only the %s one published", m.gates, tier)
			}
		})
	}
}

func TestThePublishedGateAnswersTheConformanceSuite(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			m := aMachine()
			state := stackOfTier(t, m, tier).State()
			surface := box.Surface(slug, tier)
			publish := edgeOver(m, fake.NewKeyValues()).Hooks().PublishGate

			edgeconformance.RunPublishedGate(t, edgeconformance.PublishedGate{
				Publish: func(ctx context.Context, published *gate.Gate) error {
					return publish(ctx, edge.GatePublication{State: state, Gate: published})
				},
				Read: func(context.Context) (*gate.Gate, error) {
					if held, found := m.gates[surface]; found {
						return &held, nil
					}
					return nil, nil
				},
			})
		})
	}
}

func TestReconcilingAStackHandsTheSwitchboardTheGateTheDeployRecordedBeforeAnyMove(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()
			m := aMachine()
			surface := box.Surface(slug, tier)
			front := edgeOver(m, fake.NewKeyValues())
			prior := edge.StackState{}
			if tier == environment.TierPreview {
				prior.GlobalPreview = previewBase
			}

			if _, err := front.Reconcile(context.Background(), edge.StackSpec{
				Version: "test", Tier: tier, Slug: slug, Gate: &recordedGate,
			}, prior); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}
			if got, held := m.gates[surface]; !held || got.Key != recordedGate.Key {
				t.Fatalf("after a protected deploy's edge step the box holds gate %+v for %s, want %+v: a deploy that fails before it moves a pointer would leave what is already served ungated", got, surface, recordedGate)
			}

			if _, err := front.Reconcile(context.Background(), edge.StackSpec{
				Version: "test", Tier: tier, Slug: slug,
			}, prior); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}
			if got, held := m.gates[surface]; held {
				t.Errorf("after an open deploy's edge step the box still holds gate %+v for %s, want it taken off", got, surface)
			}
		})
	}
}

type purges struct {
	m      *machine
	purged [][]string
	err    error
}

func (p *purges) purge(_ context.Context, hostnames []string) error {
	p.m.calls = append(p.m.calls, "purge "+strings.Join(hostnames, ","))
	p.purged = append(p.purged, slices.Clone(hostnames))
	return p.err
}

func TestAGateBehindAnEdgeThatCachesLandsOnTheBoxBeforeWhatTheEdgeCachedIsPurged(t *testing.T) {
	t.Parallel()
	bound := edge.StackState{Slug: slug, Tier: environment.TierProduction, Bound: []string{productionHost, "www.shop.example.com"}}
	for name, put := range map[string]func(*box.Edge, box.Purge) error{
		"reconcile": func(e *box.Edge, purge box.Purge) error {
			return e.ReconcileGateBehind(context.Background(), edge.StackSpec{Tier: environment.TierProduction, Slug: slug, Gate: &productionGate}, bound, purge)
		},
		"publish": func(e *box.Edge, purge box.Purge) error {
			return e.PublishGateBehind(purge)(context.Background(), edge.GatePublication{State: bound, Gate: &productionGate})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := aMachine()
			cache := &purges{m: m}

			if err := put(edgeOver(m, fake.NewKeyValues()).Edge, cache.purge); err != nil {
				t.Fatalf("putting the gate = %v", err)
			}

			surface := box.Surface(slug, environment.TierProduction)
			if got := m.gates[surface]; got.Key != productionGate.Key {
				t.Fatalf("the box holds gate %+v for %s, want %+v", got, surface, productionGate)
			}
			gated, purged := indexOf(m.calls, "gate "+surface), indexOf(m.calls, "purge ")
			if gated < 0 || purged < 0 || purged < gated {
				t.Errorf("calls = %v, want the gate on the box and then the edge's cache purged: a cache purged first refills from the ungated box", m.calls)
			}
			if len(cache.purged) != 1 || !slices.Equal(cache.purged[0], bound.Bound) {
				t.Errorf("purged %v, want every hostname the stack binds (%v) so no copy cached before the gate answers a visitor", cache.purged, bound.Bound)
			}
		})
	}
}

func TestOpeningAStackBehindAnEdgeThatCachesPurgesNothing(t *testing.T) {
	t.Parallel()

	m := aMachine()
	cache := &purges{m: m}
	surface := box.Surface(slug, environment.TierProduction)
	m.gates[surface] = productionGate
	state := edge.StackState{Slug: slug, Tier: environment.TierProduction, Bound: []string{productionHost}}

	if err := edgeOver(m, fake.NewKeyValues()).PublishGateBehind(cache.purge)(context.Background(), edge.GatePublication{State: state}); err != nil {
		t.Fatalf("PublishGateBehind() = %v", err)
	}

	if _, held := m.gates[surface]; held {
		t.Errorf("the box still holds a gate for %s, want it taken off", surface)
	}
	if len(cache.purged) != 0 {
		t.Errorf("purged %v, want nothing: nothing an open stack serves needs keeping out of the cache", cache.purged)
	}
}

func TestAGateBehindAnEdgeWhoseCacheCannotBePurgedFailsSoTheDeployOrSyncTriesAgain(t *testing.T) {
	t.Parallel()

	m := aMachine()
	cache := &purges{m: m, err: errors.New("the zone refused the purge")}
	state := edge.StackState{Slug: slug, Tier: environment.TierProduction, Bound: []string{productionHost}}
	publish := edgeOver(m, fake.NewKeyValues()).PublishGateBehind(cache.purge)

	if err := publish(context.Background(), edge.GatePublication{State: state, Gate: &productionGate}); err == nil {
		t.Error("PublishGateBehind() = nil, want the failed purge to fail the publication")
	}
	if err := edgeOver(m, fake.NewKeyValues()).PublishGateBehind(nil)(context.Background(), edge.GatePublication{State: state, Gate: &productionGate}); err == nil {
		t.Error("PublishGateBehind(nil) = nil, want an edge that cannot purge refused once it binds hostnames")
	}
	if got := m.gates[box.Surface(slug, environment.TierProduction)]; got.Key != productionGate.Key {
		t.Errorf("the box holds gate %+v, want the gate kept on the box even though the purge failed", got)
	}
}
