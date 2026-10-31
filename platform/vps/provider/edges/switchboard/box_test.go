package switchboard_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/edges/switchboard"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

const (
	address  = "203.0.113.10"
	sshScope = "ada@ocelbox"
	slug     = "shop"
)

type fakeBox struct {
	pins        []boxapi.Pin
	claims      []boxapi.Claim
	shields     []boxapi.Shield
	upstream    map[boxapi.RouteKey]string
	swept       map[string]bool
	present     map[string]bool
	runs        map[string]release.Run
	archived    map[string]bool
	calls       []string
	rollouts    []release.Rollout
	started     []release.Container
	headed      []string
	previewBase string
	refusals    map[string]error
	visited     []string
	rollingOut  func(release.Rollout) error
	discarded   []string
	discards    []release.Rollout
	leftRunning string
	byHand      int
	tables      int

	tunnel          *boxapi.Tunnel
	retired         []boxapi.Tunnel
	previewTunneled bool
	tunnelRunning   string
	tunnelsReserved int
	tunnelEvents    tunnelEvents

	recordedGates map[string]gate.Gate
	gates         map[string]gate.Gate
	gateReads     []environment.Tier

	promotedWorkers []string
}

func aMachine() *fakeBox {
	return &fakeBox{
		upstream: map[boxapi.RouteKey]string{},
		swept:    map[string]bool{},
		present:  map[string]bool{},
		runs:     map[string]release.Run{},
		archived: map[string]bool{},
		refusals: map[string]error{},

		recordedGates: map[string]gate.Gate{},
		gates:         map[string]gate.Gate{},
	}
}

func (m *fakeBox) refuseOn(call string, err error) { m.refusals[call] = err }

func (m *fakeBox) refuse(call string) error {
	m.visited = append(m.visited, call)
	return m.refusals[call]
}

func (m *fakeBox) reached() []string {
	return slices.Compact(slices.Sorted(slices.Values(m.visited)))
}

func (m *fakeBox) Address(context.Context) (string, error) {
	return address, m.refuse("Address")
}

func (m *fakeBox) Pins() *certs.Pins { return certs.NewPins(nil, m.pins) }

func (m *fakeBox) RouteBy(hostname string) string {
	if m.byHand == 0 {
		return ""
	}
	return manual.Route(hostname, m.byHand)
}

func (m *fakeBox) Ensure(_ context.Context, project, app, coordinate string) (bool, error) {
	if err := m.refuse("EnsureImage"); err != nil {
		return false, err
	}
	return !m.swept[coordinate] || m.archived[project+"/"+app+" "+coordinate], nil
}

func (m *fakeBox) Run(_ context.Context, spec release.Container) (release.Run, error) {
	m.calls = append(m.calls, "run "+spec.Name)
	m.started = append(m.started, spec)
	ran, told := m.runs[spec.Name]
	if !told && !m.present[spec.Name] {
		ran = release.RunCreated
	}
	if err := errors.Join(m.refuse("RunContainer"), m.refuse("RunContainer "+spec.Name)); err != nil {
		return ran, err
	}
	m.present[spec.Name] = true
	return ran, nil
}

func (m *fakeBox) ForgetNetwork(_ context.Context, tier environment.Tier, project string) error {
	m.calls = append(m.calls, "forget network "+string(tier)+"/"+project)
	return m.refuse("ForgetNetwork")
}

func (m *fakeBox) Promote(_ context.Context, _ environment.Tier, project, env, app, coordinate string) error {
	m.calls = append(m.calls, "head "+project+"/"+app+" in "+env+" at "+coordinate)
	m.headed = append(m.headed, coordinate)
	return m.refuse("Promote")
}

type machineWorkers struct{ m *fakeBox }

func (w machineWorkers) Promote(_ context.Context, tier environment.Tier, project, env, app, appContainer string, _ progress.Log) error {
	w.m.promotedWorkers = append(w.m.promotedWorkers, string(tier)+" "+project+"/"+app+" in "+env+" beside "+appContainer)
	return w.m.refuse("PromoteWorkers")
}

func (m *fakeBox) Serving(_ context.Context, key boxapi.RouteKey) (string, error) {
	m.calls = append(m.calls, "serving "+key.App)
	if err := m.refuse("Serving"); err != nil {
		return "", err
	}
	return m.upstream[key], nil
}

func (m *fakeBox) RollOut(_ context.Context, rollout release.Rollout, _ progress.Log) error {
	if len(rollout.Apps) == 0 {
		return nil
	}
	m.rollouts = append(m.rollouts, rollout)
	onto := make([]string, 0, len(rollout.Apps))
	for _, app := range rollout.Apps {
		onto = append(onto, app.App+" onto "+app.Target)
	}
	m.calls = append(m.calls, "release "+strings.Join(onto, ", "))
	if m.rollingOut != nil {
		if err := m.rollingOut(rollout); err != nil {
			return err
		}
	}
	if err := m.refuse("Release"); err != nil {
		return err
	}
	for {
		read := m.tables
		if rollout.StillActive != nil {
			if err := rollout.StillActive(context.Background()); err != nil {
				return router.Unserved{Err: err}
			}
		}
		if m.tables != read {
			continue
		}
		for _, app := range rollout.Apps {
			m.upstream[app.RouteKey] = app.Target
		}
		m.tables++
		return nil
	}
}

func (m *fakeBox) Discard(_ context.Context, rollout release.Rollout) string {
	targets := make([]string, 0, len(rollout.Apps))
	for _, app := range rollout.Apps {
		targets = append(targets, app.Target)
	}
	m.calls = append(m.calls, "discard "+strings.Join(targets, ", "))
	m.discarded = append(m.discarded, targets...)
	m.discards = append(m.discards, rollout)
	return m.leftRunning
}

func (m *fakeBox) UnroutePointer(_ context.Context, owner, pointer string) error {
	m.calls = append(m.calls, "unroute "+owner+"/"+pointer)
	if err := m.refuse("UnroutePointer"); err != nil {
		return err
	}
	maps.DeleteFunc(m.upstream, func(key boxapi.RouteKey, _ string) bool {
		return key.Owner == owner && key.Pointer == pointer
	})
	m.tables++
	return nil
}

func (m *fakeBox) UnrouteSurface(_ context.Context, owner string) error {
	m.calls = append(m.calls, "unroute "+owner)
	if err := m.refuse("UnrouteSurface"); err != nil {
		return err
	}
	maps.DeleteFunc(m.upstream, func(key boxapi.RouteKey, _ string) bool { return key.Owner == owner })
	m.tables++
	return nil
}

func (m *fakeBox) Claims(context.Context) ([]boxapi.Claim, error) {
	if err := m.refuse("Claims"); err != nil {
		return nil, err
	}
	return slices.Clone(m.claims), nil
}

func (m *fakeBox) ClaimHosts(_ context.Context, claims []boxapi.Claim) error {
	if err := m.refuse("ClaimHosts"); err != nil {
		return err
	}
	for _, claim := range claims {
		m.calls = append(m.calls, "claim "+claim.Hostname)
		taken, err := routing.Claim(m.claims, claim)
		if err != nil {
			return err
		}
		m.claims = taken
	}
	return nil
}

func (m *fakeBox) DisclaimPointer(_ context.Context, owner, pointer string) error {
	m.calls = append(m.calls, "disclaim "+owner+"/"+pointer)
	if err := m.refuse("DisclaimPointer"); err != nil {
		return err
	}
	m.claims = routing.Disclaim(m.claims, func(claim boxapi.Claim) bool {
		return claim.Owner == owner && claim.Pointer == pointer
	})
	return nil
}

func (m *fakeBox) DisclaimHost(_ context.Context, hostname, owner string) error {
	m.calls = append(m.calls, "disclaim "+hostname)
	if err := m.refuse("DisclaimHost"); err != nil {
		return err
	}
	m.claims = routing.Disclaim(m.claims, func(claim boxapi.Claim) bool {
		return claim.Hostname == hostname && claim.Owner == owner
	})
	return nil
}

func (m *fakeBox) DisclaimSurface(_ context.Context, owner string) error {
	m.calls = append(m.calls, "disclaim "+owner)
	if err := m.refuse("DisclaimSurface"); err != nil {
		return err
	}
	m.claims = routing.Disclaim(m.claims, func(claim boxapi.Claim) bool { return claim.Owner == owner })
	m.shields = routing.DropShields(m.shields, func(shield boxapi.Shield) bool { return shield.Owner == owner })
	delete(m.gates, owner)
	return nil
}

func (m *fakeBox) Unpin(_ context.Context, project string) error {
	m.calls = append(m.calls, "unpin "+project)
	return m.refuse("Unpin")
}

func (m *fakeBox) PutShield(_ context.Context, shield boxapi.Shield) (boxapi.Shield, error) {
	m.calls = append(m.calls, "shield "+shield.Hostname)
	if err := m.refuse("PutShield"); err != nil {
		return boxapi.Shield{}, err
	}
	m.shields = routing.MergeShield(m.shields, shield)
	return m.shields[len(m.shields)-1], nil
}

func (m *fakeBox) PutGate(_ context.Context, owner string, held *gate.Gate) error {
	m.calls = append(m.calls, "gate "+owner)
	if err := m.refuse("PutGate"); err != nil {
		return err
	}
	delete(m.gates, owner)
	if held != nil {
		m.gates[owner] = *held
	}
	return nil
}

func (m *fakeBox) record(tier environment.Tier, project string, held gate.Gate) {
	m.recordedGates[switchboard.Surface(project, tier)] = held
}

func (m *fakeBox) readGate(_ context.Context, tier environment.Tier, project string) (*gate.Gate, error) {
	m.gateReads = append(m.gateReads, tier)
	if err := m.refuse("ReadGate"); err != nil {
		return nil, err
	}
	held, recorded := m.recordedGates[switchboard.Surface(project, tier)]
	if !recorded {
		return nil, nil
	}
	return &held, nil
}

func (m *fakeBox) RefuseUnshielded(context.Context, string) error {
	return m.refuse("RefuseUnshielded")
}

func (m *fakeBox) RemoveShield(_ context.Context, hostname, owner string) error {
	m.calls = append(m.calls, "unshield "+hostname)
	if err := m.refuse("RemoveShield"); err != nil {
		return err
	}
	m.shields = routing.DropShields(m.shields, func(shield boxapi.Shield) bool { return shield.Hostname == hostname && shield.Owner == owner })
	return nil
}

type boxEdge struct {
	*switchboard.Edge
	keyValues records.Store
}

func edgeOver(m *fakeBox, store records.Store) boxEdge {
	return boxEdge{Edge: switchboard.New(switchboard.Box{Machine: m, Containers: m, Images: m, Workers: machineWorkers{m}, Routes: m, Tunnel: machineTunnel{m}, Origins: m.ApplyOrigins, Scope: sshScope, Tunnels: m.findTunnelHooks, ReadGate: m.readGate}), keyValues: store}
}

type boxStack struct {
	edge.EdgeStack
	router.Stack
	ledger *ledger.Ledger
}

func (s boxStack) State() edge.StackState { return s.EdgeStack.State() }

func (s boxStack) Destroy(ctx context.Context) error { return s.EdgeStack.Destroy(ctx) }

func (s boxStack) Ledger() *ledger.Ledger { return s.ledger }

func stackOn(front boxEdge, stack edge.EdgeStack) boxStack {
	opened, err := switchboard.NewRouter(front.Edge).Open(router.NewStackState(stack.State()))
	if err != nil {
		panic(err)
	}
	state := stack.State()
	releases := ledger.New(front.keyValues, state.Tier, state.Slug)
	return boxStack{EdgeStack: stack, Stack: fake.PromotingStack{Stack: opened, Ledger: releases}, ledger: releases}
}

func removePointer(ctx context.Context, stack boxStack, pointer string, progress progress.Log) (router.PruneResult, error) {
	if err := stack.RemovePointer(ctx, router.PointerRemoval{Pointer: pointer}, progress); err != nil {
		return router.PruneResult{}, err
	}
	removed, _, err := stack.Ledger().RemovePointer(ctx, pointer)
	return removed, err
}

func (m *fakeBox) ApplyOrigins(_ context.Context, project string, tier environment.Tier) error {
	m.calls = append(m.calls, "apply origins "+project+"/"+string(tier))
	return m.refuse("ApplyOrigins")
}

func (m *fakeBox) PreviewEntry(context.Context) (string, error) {
	return m.previewBase, m.refuse("PreviewEntry")
}

func (m *fakeBox) InstallPreviewEntry(_ context.Context, base string) error {
	m.calls = append(m.calls, "install preview entry "+base)
	if err := m.refuse("InstallPreviewEntry"); err != nil {
		return err
	}
	if m.previewBase != "" && m.previewBase != base {
		return refusal.Refuse(refusal.CodeBusy,
			"this box already answers previews on %s", edge.PreviewWildcard(m.previewBase))
	}
	m.previewBase, m.previewTunneled = base, false
	return nil
}

func (m *fakeBox) RemovePreviewEntry(_ context.Context, base string) error {
	m.calls = append(m.calls, "remove preview entry "+base)
	if err := m.refuse("RemovePreviewEntry"); err != nil {
		return err
	}
	if m.previewBase == base {
		m.previewBase, m.previewTunneled = "", false
	}
	return nil
}

func TestTheBoxEdgeBehavesAsEveryEdgeMust(t *testing.T) {
	edgeconformance.Run(t, edgeconformance.Suite{
		Hostname: "shop.example.com",
		New: func(*testing.T) (edge.Edge, edge.StackSpec) {
			return edgeOver(aMachine(), fake.NewKeyValues()),
				edge.StackSpec{Version: "test", Tier: environment.TierProduction, Slug: slug}
		},
		Previews: func(*testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec) {
			return edgeOver(aMachine(), fake.NewKeyValues()),
				edge.StackSpec{Version: "test", Tier: environment.TierPreview, Slug: slug},
				previewSpec()
		},
	})
}

func TestTheBoxRouterBehavesAsEveryRouterMust(t *testing.T) {
	routerconformance.Run(t, routerconformance.Suite{
		Hostname:    "shop.example.com",
		PreviewBase: "preview.example.com",
		Tunnel:      tunnelEdge,
		New: func(t *testing.T) routerconformance.Fixture {
			m, front, stack := reconciled(t)
			return boxFixture(m, front, stack)
		},
		Previews: func(t *testing.T) routerconformance.Fixture {
			m := aMachine()
			front := edgeOver(m, fake.NewKeyValues())
			return boxFixture(m, front, previewStackOn(t, front))
		},
		Record: func(app, build string) router.ReleaseRecord {
			return router.ReleaseRecord{
				App: app, Release: build, RootFunction: "/", Image: imageFor(app, build),
				Physical: app + "-" + build, HealthPath: "/healthz",
			}
		},
	})
}

func boxFixture(m *fakeBox, front boxEdge, stack boxStack) routerconformance.Fixture {
	state := stack.State()
	return routerconformance.Fixture{
		Router: switchboard.NewRouter(front.Edge),
		Spec:   router.StackSpec{Tier: state.Tier, Slug: state.Slug},
		Prior:  router.NewStackState(state),
		Serving: func(pointer string) string {
			if pointer == "" {
				pointer = router.DefaultPointer
			}
			target := m.upstream[boxapi.RouteKey{Owner: switchboard.Surface(state.Slug, state.Tier), Pointer: pointer, App: routerconformance.App}]
			physical, _, _ := strings.Cut(target, ":")
			return strings.TrimPrefix(physical, routerconformance.App+"-")
		},
		FailNextPointerMove: func(err error) { m.refuseOn("Release", router.Unserved{Err: err}) },
	}
}

func reconciled(t *testing.T) (*fakeBox, boxEdge, boxStack) {
	t.Helper()

	m := aMachine()
	front := edgeOver(m, fake.NewKeyValues())
	stackEdge, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Tier: environment.TierProduction, Slug: slug,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	m.calls = nil
	stack := stackOn(front, stackEdge)
	return m, front, stack
}

func staged(t *testing.T, stack boxStack, app, identity, physical string) {
	t.Helper()

	if err := stack.Ledger().PutStaged(context.Background(), router.ReleaseRecord{
		App:          app,
		Release:      identity,
		RootFunction: "/",
		Image:        imageFor(app, identity),
		Physical:     physical,
		HealthPath:   "/healthz",
	}); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
}

func imageFor(app, identity string) string { return "ghcr.io/acme/" + app + ":" + identity }

func TestTheEdgeAnswersTheFactsABoxCanSitBehind(t *testing.T) {
	t.Parallel()

	front := edgeOver(aMachine(), fake.NewKeyValues())
	facts := front.Facts()
	if facts.RunsCode || switchboard.NewRouter(front.Edge).Facts().SignsOriginForwards || facts.InvalidatesByCacheTag {
		t.Errorf("Facts() = %+v; a box runs the container and nothing in front of it, its origin is one hop on a private network that verifies no signature, and there is no edge cache to tag", facts)
	}
	if facts.ServesUnbound {
		t.Error("Facts().ServesUnbound = true; a box answers only the hostnames something on it claims, and DNS for one points at the box's own address")
	}
	if facts.CredentialScope != sshScope {
		t.Errorf("Facts().CredentialScope = %q, want the ssh destination %q it is reached at", facts.CredentialScope, sshScope)
	}
	if !slices.Equal(facts.Supported, []edge.Need{edge.NeedStreaming, edge.NeedRevalidation}) {
		t.Errorf("Facts().Supported = %v, want streaming and revalidation alone", facts.Supported)
	}
}

func TestTheCutoverReturnsNoPropagationNoteToPrint(t *testing.T) {
	t.Parallel()

	bound := switchboard.NewRouter(edgeOver(aMachine(), fake.NewKeyValues()).Edge).Facts().Propagation
	if bound.Typical > 0 {
		t.Errorf("Facts().Propagation = %+v, and a propagation above zero is rendered to the user as a propagation note; when the cutover call returns on a box the gate has passed, the config is loaded and the retired upstream has drained, so there is no window to advertise", bound)
	}
	if bound.Published {
		t.Errorf("Facts().Propagation = %+v, which publishes a propagation it declares instant", bound)
	}
}

func TestBootstrappingTheEdgeTouchesTheBoxNotAtAll(t *testing.T) {
	t.Parallel()

	m := aMachine()
	front := edgeOver(m, fake.NewKeyValues())
	out, err := front.Bootstrap(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if out.Trust != edge.TrustInternal {
		t.Errorf("Bootstrap trust = %q, want %q: the proxy and everything it depends on are the box bootstrap's items", out.Trust, edge.TrustInternal)
	}
	if err := front.Teardown(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(m.calls) != 0 {
		t.Errorf("bootstrapping and tearing the edge down reached the box (%v); the proxy, its baseline, its helper and its data directory are bootstrap items and not edge methods", m.calls)
	}
}

func TestPromoteEnsuresTheContainerIsRunningBeforeItCutsOver(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")

	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	want := []string{"gate " + switchboard.Surface(slug, environment.TierProduction), "run shop-web-1111", "release web onto shop-web-1111:" + containerimage.PortText, "head shop/web in prod at " + imageFor("web", "b1")}
	if !slices.Equal(m.calls, want) {
		t.Fatalf("Promote drove the box as %v, want %v: it makes the promotion's containers running, then cuts over, and heads the window only at what now serves", m.calls, want)
	}
	if m.rollouts[0].Apps[0].HealthPath != "/healthz" {
		t.Errorf("the release is gated on %q, want the path the record names: up is a 2xx on the path the deploy request named", m.rollouts[0].Apps[0].HealthPath)
	}
}

func TestAPromotionPointsTheEnvironmentsWorkersAtTheReleaseItServesOnceItServes(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	for _, promotion := range []struct{ id, build string }{{"p1", "b1"}, {"p2", "b2"}, {"p3", "b1"}} {
		if err := promoted(t, stack, promotion.id, "web", promotion.build); err != nil {
			t.Fatalf("Promote(%s): %v", promotion.id, err)
		}
	}

	want := []string{
		"production shop/web in prod beside shop-web-1111",
		"production shop/web in prod beside shop-web-2222",
		"production shop/web in prod beside shop-web-1111",
	}
	if !slices.Equal(m.promotedWorkers, want) {
		t.Errorf("the promotions pointed the workers at %v, want %v: each promotion, a rollback among them, moves the workers to the release it serves", m.promotedWorkers, want)
	}
}

func TestAPromotionTheBoxNeverServedLeavesTheWorkersOnTheReleaseItServes(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	m.refuseOn("Release", router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady, "the gate exited 4; the previous release is still live")})

	if err := promoted(t, stack, "p2", "web", "b2"); err == nil {
		t.Fatal("a promotion the gate refused succeeded")
	}
	if want := []string{"production shop/web in prod beside shop-web-1111"}; !slices.Equal(m.promotedWorkers, want) {
		t.Errorf("the workers were pointed at %v, want %v alone: the box still serves p1", m.promotedWorkers, want)
	}
}

func TestAPromotionWhoseWorkersCannotStartReportsTheBoxUnrecorded(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	m.refuseOn("PromoteWorkers", refusal.Refuse(refusal.CodeNotReady, "shop-prod-web-worker-1111 is gone from this box"))

	err := promoted(t, stack, "p1", "web", "b1")
	if err == nil || !strings.Contains(err.Error(), "is gone from this box") {
		t.Errorf("Promote(p1) = %v, want it refused with why the workers did not follow", err)
	}
}

func TestAPromotionOfARecordWhosePathWasDiscoveredSaysSoToTheGate(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	if err := stack.Ledger().PutStaged(context.Background(), router.ReleaseRecord{
		App: "web", Release: "b1", RootFunction: "/", Image: imageFor("web", "b1"), Physical: "shop-web-1111",
		HealthPath: "/up", HealthPathDiscovered: true,
	}); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if got := m.rollouts[0].Apps[0]; got.HealthPath != "/up" || !got.HealthPathDiscovered {
		t.Errorf("the release is gated on %q (discovered %t), want /up marked discovered: a failed gate then says the path came from probing", got.HealthPath, got.HealthPathDiscovered)
	}
}

func TestAPromotionOfSeveralAppsCutsOverThemAllInOneRelease(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "api", "b1", "shop-api-1111")

	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1", "api": "b1"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	want := []string{
		"gate " + switchboard.Surface(slug, environment.TierProduction),
		"run shop-api-1111", "run shop-web-1111",
		"release api onto shop-api-1111:" + containerimage.PortText + ", web onto shop-web-1111:" + containerimage.PortText,
		"head shop/api in prod at " + imageFor("api", "b1"), "head shop/web in prod at " + imageFor("web", "b1"),
	}
	if !slices.Equal(m.calls, want) {
		t.Fatalf("a promotion of two apps drove the box as %v, want %v: every container is running before one release cuts over them all, so a failure on either leaves the box on the promotion it was serving", m.calls, want)
	}
}

func TestAPromotionTheBoxNeverServedLeavesThePointerOnTheOneItServes(t *testing.T) {
	t.Parallel()

	for what, refuse := range map[string]func(*fakeBox){
		"a container that would not run": func(m *fakeBox) {
			m.refuseOn("RunContainer", refusal.Refuse(refusal.CodeNotReady, "docker run failed"))
		},
		"a release the box kept off": func(m *fakeBox) {
			m.refuseOn("Release", router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady, "the gate exited 4; the previous release is still live")})
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			m, _, stack := reconciled(t)
			staged(t, stack, "web", "b1", "shop-web-1111")
			staged(t, stack, "web", "b2", "shop-web-2222")
			if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
				t.Fatalf("Promote(p1): %v", err)
			}
			refuse(m)

			if err := promoted(t, stack, "p2", "web", "b2"); err == nil {
				t.Fatalf("a promotion refused by %s succeeded", what)
			}
			if active := activePromotion(t, stack); active != "p1" {
				t.Errorf("after %s the pointer is at %q, want p1: the box still serves p1, and a rollback read from a pointer at p2 re-points from a release that never served", what, active)
			}
		})
	}
}

func TestAPromotionThatFailedAfterTheCutoverKeepsThePointerOnTheReleaseTheBoxServes(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	m.refuseOn("Release", refusal.Refuse(refusal.CodeNotReady, "cut over to shop-web-2222, which now serve, but shop-web-1111 was drained and unrouted but not stopped"))

	if err := promoted(t, stack, "p2", "web", "b2"); err == nil {
		t.Fatal("a promotion whose retiree would not stop after the cutover reported success")
	}
	if active := activePromotion(t, stack); active != "p2" {
		t.Errorf("after a failure past the cutover the pointer is at %q, want p2: the box is serving p2", active)
	}
}

func TestAPromotionThatFailedAfterTheCutoverHeadsTheWindowAtTheReleaseTheBoxServes(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	m.refuseOn("Release", refusal.Refuse(refusal.CodeNotReady, "cut over to shop-web-2222, which now serve, but shop-web-1111 was drained and unrouted but not stopped"))

	err := promoted(t, stack, "p2", "web", "b2")
	if err == nil {
		t.Fatal("a promotion whose retiree would not stop after the cutover reported success")
	}
	if !strings.Contains(err.Error(), "shop-web-1111 was drained and unrouted but not stopped") {
		t.Errorf("the promotion failed with\n%s\nand never says what past the cutover did not finish", err)
	}
	if len(m.headed) == 0 || m.headed[len(m.headed)-1] != imageFor("web", "b2") {
		t.Errorf("the box serves p2 (b2) but its window heads at %v: the next deploy's reconcile sweeps the image the box serves", m.headed)
	}
}

func TestAPromotionRefusedBeforeItsRollOutRemovesOnlyTheContainersItCreated(t *testing.T) {
	t.Parallel()

	for what, refused := range map[string]struct {
		refuse func(*fakeBox)
		want   []string
	}{
		"a container that would not run": {
			refuse: func(m *fakeBox) {
				m.refuseOn("RunContainer", refusal.Refuse(refusal.CodeNotReady, "docker run failed"))
			},
			want: []string{"shop-api-1111:" + containerimage.PortText},
		},
		"a gate the box would not take": {
			refuse: func(m *fakeBox) {
				m.refuseOn("PutGate", refusal.Refuse(refusal.CodeNotReady, "the routing table could not be written"))
			},
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			m, _, stack := reconciled(t)
			staged(t, stack, "web", "b1", "shop-web-1111")
			staged(t, stack, "api", "b1", "shop-api-1111")
			refused.refuse(m)
			m.leftRunning = "\nshop-api-1111 left running: the daemon went away"

			err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
				PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1", "api": "b1"},
			}}, progress.Discard())
			if err == nil {
				t.Fatalf("a promotion refused by %s succeeded", what)
			}
			if !slices.Equal(m.discarded, refused.want) {
				t.Errorf("after %s the box discarded %v, want %v: a refused promotion removes the containers it created and nothing it never reached", what, m.discarded, refused.want)
			}
			if len(refused.want) > 0 && !strings.Contains(err.Error(), "shop-api-1111 left running: the daemon went away") {
				t.Errorf("the refusal reads\n%s\nand never names the container the box could not remove", err)
			}
		})
	}
}

func TestARollbackRefusedBeforeItsRollOutKeepsTheContainersItRollsBackTo(t *testing.T) {
	t.Parallel()

	for what, refuse := range map[string]func(*fakeBox){
		"a gate the box would not take": func(m *fakeBox) {
			m.refuseOn("PutGate", refusal.Refuse(refusal.CodeNotReady, "the routing table could not be written"))
		},
		"a container that would not run again": func(m *fakeBox) {
			m.refuseOn("RunContainer shop-web-1111", refusal.Refuse(refusal.CodeNotReady, "docker start failed"))
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			m, _, stack := reconciled(t)
			for _, app := range []string{"api", "web"} {
				staged(t, stack, app, "b1", "shop-"+app+"-1111")
				staged(t, stack, app, "b2", "shop-"+app+"-2222")
			}
			for _, promotion := range []router.Promotion{
				{PromotionID: "p1", Ts: 1, Releases: map[string]string{"api": "b1", "web": "b1"}},
				{PromotionID: "p2", Ts: 2, Releases: map[string]string{"api": "b2", "web": "b2"}},
			} {
				if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
					t.Fatalf("Promote(%s): %v", promotion.PromotionID, err)
				}
			}
			refuse(m)

			err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
				PromotionID: "p3", Ts: 3, Releases: map[string]string{"api": "b1", "web": "b1"},
			}}, progress.Discard())
			if err == nil {
				t.Fatalf("a rollback refused by %s succeeded", what)
			}
			if len(m.discarded) != 0 {
				t.Errorf("after %s the box discarded %v: the rollback created neither, and a retired container removed is a release that can never be rolled back to", what, m.discarded)
			}
		})
	}
}

func TestARollbackRefusedBeforeItsRollOutStopsTheContainersItStartedAgain(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	for _, app := range []string{"api", "web"} {
		staged(t, stack, app, "b1", "shop-"+app+"-1111")
		staged(t, stack, app, "b2", "shop-"+app+"-2222")
	}
	for _, promotion := range []router.Promotion{
		{PromotionID: "p1", Ts: 1, Releases: map[string]string{"api": "b1", "web": "b1"}},
		{PromotionID: "p2", Ts: 2, Releases: map[string]string{"api": "b2", "web": "b2"}},
	} {
		if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
			t.Fatalf("Promote(%s): %v", promotion.PromotionID, err)
		}
	}
	m.runs["shop-api-1111"] = release.RunRestarted
	m.refuseOn("RunContainer shop-web-1111", refusal.Refuse(refusal.CodeNotReady, "docker start failed"))

	err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p3", Ts: 3, Releases: map[string]string{"api": "b1", "web": "b1"},
	}}, progress.Discard())
	if err == nil {
		t.Fatal("a rollback whose second container would not start again succeeded")
	}
	if len(m.discards) != 1 || len(m.discards[0].Apps) != 1 ||
		m.discards[0].Apps[0].Target != "shop-api-1111:"+containerimage.PortText || m.discards[0].Apps[0].Run != release.RunRestarted {
		t.Errorf("the refused rollback discarded %+v, want only shop-api-1111, as a container it started again: the box found it stopped, so it stops it again and keeps the values a later rollback needs", m.discards)
	}
}

func TestARollbackTellsItsRollOutWhichContainersItStartedAgain(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	for _, promotion := range []router.Promotion{
		{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1"}},
		{PromotionID: "p2", Ts: 2, Releases: map[string]string{"web": "b2"}},
	} {
		if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
			t.Fatalf("Promote(%s): %v", promotion.PromotionID, err)
		}
	}
	m.runs["shop-web-1111"] = release.RunRestarted

	if err := promoted(t, stack, "p3", "web", "b1"); err != nil {
		t.Fatalf("Promote(rollback): %v", err)
	}
	last := m.rollouts[len(m.rollouts)-1]
	if len(last.Apps) != 1 || last.Apps[0].Run != release.RunRestarted {
		t.Errorf("the rollback handed its roll-out %+v, which does not say shop-web-1111 was started again: a roll-out refused at its gate then removes the container a rollback exists to keep", last.Apps)
	}
}

func TestARefusedPromotionLeavesTheBoxsWindowHeadedWhereItWas(t *testing.T) {
	t.Parallel()

	for what, refuse := range map[string]func(*fakeBox){
		"a container that would not run": func(m *fakeBox) {
			m.refuseOn("RunContainer", refusal.Refuse(refusal.CodeNotReady, "docker run failed"))
		},
		"a release the box kept off": func(m *fakeBox) {
			m.refuseOn("Release", router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady, "the gate exited 4; the previous release is still live")})
		},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			m, _, stack := reconciled(t)
			staged(t, stack, "web", "b1", "shop-web-1111")
			staged(t, stack, "web", "b2", "shop-web-2222")
			if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
				t.Fatalf("Promote(p1): %v", err)
			}
			refuse(m)

			if err := promoted(t, stack, "p2", "web", "b2"); err == nil {
				t.Fatalf("a promotion refused by %s succeeded", what)
			}
			if want := []string{imageFor("web", "b1")}; !slices.Equal(m.headed, want) {
				t.Errorf("after %s the box's window was headed at %v, want %v: the box still serves b1, and a window heading a release that never served disagrees with the proxy and the ledger", what, m.headed, want)
			}
		})
	}
}

func TestARollOutTheHostRefusedIsNotDiscardedASecondTimeByTheBox(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	m.refuseOn("Release", router.Unserved{Err: refusal.Refuse(refusal.CodeNotReady, "the gate exited 4; the previous release is still live\nshop-web-1111 removed")})

	if err := promoted(t, stack, "p1", "web", "b1"); err == nil {
		t.Fatal("a promotion whose roll-out was refused succeeded")
	}
	if len(m.discarded) != 0 {
		t.Errorf("the box discarded %v after the host refused the roll-out, which removes what it stood itself", m.discarded)
	}
}

func TestAPromotionTheBoxServesButCannotHeadItsWindowAtKeepsThePointerOnIt(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	m.refuseOn("Promote", refusal.Refuse(refusal.CodeNotReady, "the releases helper failed"))

	err := promoted(t, stack, "p1", "web", "b1")
	if err == nil {
		t.Fatal("a promotion whose window could not be headed reported success")
	}
	if unserved := (router.Unserved{}); errors.As(err, &unserved) {
		t.Errorf("the promotion failed with %v, which says the box never served it, yet the proxy routes to shop-web-1111", err)
	}
	if active := activePromotion(t, stack); active != "p1" {
		t.Errorf("the pointer is at %q, want p1: the box serves p1", active)
	}
}

func TestAPromotionOvertakenWhileItGatedNeverCutsOverTheBoxAwayFromTheOneThatOvertookIt(t *testing.T) {
	t.Parallel()

	m := aMachine()
	store := fake.NewKeyValues()
	front := edgeOver(m, store)
	stackEdge, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Tier: environment.TierProduction, Slug: slug,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	stack := stackOn(front, stackEdge)
	for identity, physical := range map[string]string{"b1": "shop-web-1111", "b2": "shop-web-2222", "b3": "shop-web-3333"} {
		staged(t, stack, "web", identity, physical)
	}
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	m.rollingOut = func(rollout release.Rollout) error {
		m.rollingOut = nil
		overtaking := ledger.New(store, environment.TierProduction, slug)
		if _, err := overtaking.Promote(context.Background(), router.Promotion{PromotionID: "p3", Releases: map[string]string{"web": "b3"}}, "", "p2"); err != nil {
			t.Fatalf("Promote(p3): %v", err)
		}
		if rollout.StillActive == nil {
			return nil
		}
		if err := rollout.StillActive(context.Background()); err != nil {
			return router.Unserved{Err: err}
		}
		return nil
	}

	err = promoted(t, stack, "p2", "web", "b2")
	if err == nil {
		t.Fatal("a promotion overtaken while it gated cut over the box onto a release the ledger no longer names")
	}
	if !strings.Contains(err.Error(), "p3") {
		t.Errorf("the refusal reads %q and never names the promotion that overtook it", err)
	}
	if active := activePromotion(t, stack); active != "p3" {
		t.Errorf("the pointer is at %q, want p3, the promotion that overtook p2", active)
	}
}

func activePromotion(t *testing.T, stack boxStack) string {
	t.Helper()
	entries, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(entries, func(entry router.HistoryEntry) bool { return entry.Active })
	if at < 0 {
		return ""
	}
	return entries[at].PromotionID
}

func TestARollbackRestartsThePreviousContainerAndCutsOverToIt(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")

	ctx := context.Background()
	for _, promotion := range []router.Promotion{
		{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1"}},
		{PromotionID: "p2", Ts: 2, Releases: map[string]string{"web": "b2"}},
	} {
		if err := stack.MovePointer(ctx, router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
			t.Fatalf("Promote(%s): %v", promotion.PromotionID, err)
		}
	}
	m.calls = nil

	if err := stack.MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p3", Ts: 3, Releases: map[string]string{"web": "b1"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(rollback): %v", err)
	}

	want := []string{"gate " + switchboard.Surface(slug, environment.TierProduction), "run shop-web-1111", "release web onto shop-web-1111:" + containerimage.PortText, "head shop/web in prod at " + imageFor("web", "b1")}
	if !slices.Equal(m.calls, want) {
		t.Fatalf("a rollback drove the box as %v, want %v: nothing provisions on this path, so re-pointing at a release that is not running is a ledger edit and not a restored site", m.calls, want)
	}
}

func TestAnAppWithNoContainerOnThisBoxCutsOverNothing(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "")

	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(m.calls) != 0 {
		t.Errorf("a promotion naming no container on this box drove the proxy (%v); there is nothing here for it to put in front", m.calls)
	}
}

func TestARecordNamingAContainerAndNoHealthPathIsRefusedRatherThanGatedOnAGuess(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	if err := stack.Ledger().PutStaged(context.Background(), router.ReleaseRecord{
		App: "web", Release: "b1", Image: "ghcr.io/acme/web:b1", Physical: "shop-web-1111",
	}); err != nil {
		t.Fatal(err)
	}

	err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1"},
	}}, progress.Discard())
	if err == nil {
		t.Fatal("Promote gated a container on a health path nothing named")
	}
	if !strings.Contains(err.Error(), "health path") {
		t.Errorf("Promote refused with %q, want it to name the health path it has no value for", err)
	}
	if len(m.calls) != 0 {
		t.Errorf("the box was reached before the record was found unusable: %v", m.calls)
	}
}

func TestABoundHostnameIsClaimedOnTheProxyAndPointedAtTheBoxItself(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	m, front, stack := reconciled(t)
	ctx := context.Background()
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	owner, err := front.DomainOwner(ctx, hostname)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if owner != switchboard.Surface(slug, environment.TierProduction) {
		t.Errorf("DomainOwner(%q) = %q, want the surface the binding claimed it for", hostname, owner)
	}
	dnsRecords, err := edge.RecordsFor(edge.TargetFor(front, stack.State()), stack.State().Bound)
	if err != nil {
		t.Fatalf("RecordsFor: %v", err)
	}
	want := edge.Record{Name: hostname, Type: edge.RecordTypeA, Value: address}
	if len(dnsRecords) != 1 || dnsRecords[0] != want {
		t.Errorf("records = %v, want %v: a box answers on an address rather than a name, so the record that points at it is an A record", dnsRecords, want)
	}
	if !slices.Contains(m.calls, "claim "+hostname) {
		t.Errorf("the binding never claimed %q on the proxy (%v), and nothing on the box would then say which project answers it", hostname, m.calls)
	}
}

func TestAHostnameBoundAfterAPromotionAnswersFromTheReleasedContainer(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: hostname, App: "web"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	at := slices.IndexFunc(m.claims, func(claim boxapi.Claim) bool { return claim.Hostname == hostname })
	if at < 0 {
		t.Fatalf("the box claims %v, want %s among them", m.claims, hostname)
	}
	claim := m.claims[at]
	want := "shop-web-1111:" + containerimage.PortText
	if got := m.upstream[boxapi.RouteKey{Owner: claim.Owner, Pointer: claim.Pointer, App: claim.App}]; got != want {
		t.Errorf("%s is claimed onto a route that answers from %q, want %q: the release was promoted before the bind, and no later promotion comes to route it",
			hostname, got, want)
	}
}

func TestAHostnameOneProjectUnbindsIsOneAnotherCanBind(t *testing.T) {
	t.Parallel()

	const hostname = "moving.example.com"
	ctx := context.Background()
	m := aMachine()
	first := reconciledOn(t, m, "shop")
	if err := first.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if err := first.UnbindDomain(ctx, hostname); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}

	if err := reconciledOn(t, m, "market").BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Errorf("binding %q after the project that claimed it released it = %v; a name one project gives up comes back into circulation, or moving a domain between projects is a change only someone with a shell on the box can make", hostname, err)
	}
}

func TestAHostnameATornDownProjectClaimedIsOneAnotherCanBind(t *testing.T) {
	t.Parallel()

	const hostname = "moving.example.com"
	ctx := context.Background()
	m := aMachine()
	first := reconciledOn(t, m, "shop")
	if err := first.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	if err := first.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if err := reconciledOn(t, m, "market").BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Errorf("binding %q after the project that claimed it was torn down = %v; a destroyed surface answers for nothing, so a claim outliving it locks the name up for good", hostname, err)
	}
}

func TestATeardownTakesTheClaimsTheBoxRecordsRatherThanTheOnesItsStateRemembers(t *testing.T) {
	t.Parallel()

	const hostname = "moving.example.com"
	ctx := context.Background()
	m := aMachine()
	front := edgeOver(m, fake.NewKeyValues())
	first := reconciledOn(t, m, "shop")
	if err := first.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	forgotten := first.State()
	forgotten.Bound, forgotten.Addresses, forgotten.Address = nil, nil, ""
	reopenedEdge, err := front.Open(forgotten)
	if err != nil {
		t.Fatalf("Open a state with no binding: %v", err)
	}
	reopened := stackOn(front, reopenedEdge)
	if err := reopened.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if err := reconciledOn(t, m, "market").BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Errorf("binding %q after the project that claimed it was torn down through a state that had lost the binding = %v; the claim is written on the box before the state naming it is persisted anywhere, so a teardown that releases only what its own state remembers strands every claim that outran it", hostname, err)
	}
}

func TestAHostnameAnotherProjectStillClaimsIsRefusedNamingWhoClaimsIt(t *testing.T) {
	t.Parallel()

	const hostname = "contested.example.com"
	ctx := context.Background()
	m := aMachine()
	if err := reconciledOn(t, m, "shop").BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	err := reconciledOn(t, m, "market").BindDomain(ctx, edge.DomainBinding{Hostname: hostname})
	if err == nil {
		t.Fatalf("binding %q over the project still claiming it succeeded, and that project's site goes dark with nothing telling it why", hostname)
	}
	if !strings.Contains(err.Error(), switchboard.Surface("shop", environment.TierProduction)) {
		t.Errorf("the refusal reads %q and never names the surface claiming %q, so nobody knows where to unbind it", err, hostname)
	}
	owner, err := edgeOver(m, fake.NewKeyValues()).DomainOwner(ctx, hostname)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if want := switchboard.Surface("shop", environment.TierProduction); owner != want {
		t.Errorf("DomainOwner(%q) = %q, want %q: a refused bind leaves the claim where it was", hostname, owner, want)
	}
}

func TestTheRemovalPlanNamesTheEdgesRowsAndNotTheContainersReleasesOwn(t *testing.T) {
	t.Parallel()

	front := edgeOver(aMachine(), fake.NewKeyValues())
	groups := must(front.PlanProjectRemoval(edge.ProjectScope{
		Slug: slug, Tier: environment.TierProduction, Hostnames: []string{"shop.example.com"}, Address: address,
	}))
	if len(groups) != 1 {
		t.Fatalf("PlanProjectRemoval = %d groups, want one", len(groups))
	}
	for _, change := range groups[0].Changes {
		if change.Kind == machine.KindContainer {
			t.Errorf("the edge's removal plan names the container %q, which the release surface already names: a teardown plan that names it twice offers to remove it twice", change.Name)
		}
	}
	kinds := make([]string, 0, len(groups[0].Changes))
	for _, change := range groups[0].Changes {
		kinds = append(kinds, change.Kind)
	}
	if !slices.Contains(kinds, switchboard.RouteKind) || !slices.Contains(kinds, switchboard.CertificateKind) {
		t.Errorf("the removal rows are %v, want the routes this project claimed and the certificates it has", kinds)
	}
	for _, change := range groups[0].Changes {
		if change.Kind != switchboard.CertificateKind {
			continue
		}
		if change.Action != edge.PlanKeep {
			t.Errorf("the plan offers to %q the certificate for %s: ocel places no key material on a box, so it has the authority to remove none", change.Action, change.Name)
		}
		if !strings.Contains(change.Reason, "renew") {
			t.Errorf("the certificate row reads %q, want it to name who renews it: that is the only distinction an operator has to act on", change.Reason)
		}
	}
}

func TestTheKeptCertificateIsNamedByTheHandleThatStoresItAndSaysWhoRenewsIt(t *testing.T) {
	t.Parallel()

	const pinned = "pr-7.preview.example.com"
	at := boxapi.PinsDir + "/wildcard"
	m := aMachine()
	m.pins = []boxapi.Pin{{Hostname: "*.preview.example.com", Path: at}}
	front := edgeOver(m, fake.NewKeyValues())

	kept := map[string]edge.PlanChange{}
	for _, change := range must(front.PlanProjectRemoval(edge.ProjectScope{
		Slug: slug, Tier: environment.TierProduction, Hostnames: []string{"shop.example.com", pinned}, Address: address,
	}))[0].Changes {
		if change.Kind == switchboard.CertificateKind {
			kept[change.Name] = change
		}
	}

	row, named := kept[certs.PinHandle(at)]
	if !named {
		t.Fatalf("the rows kept are %v, want the pair covering %s named by the handle that stores it: naming it by hostname says this box's proxy obtained and renews a certificate the operator placed, which is the wrong-wording bug in the other direction", slices.Sorted(maps.Keys(kept)), pinned)
	}
	if !strings.Contains(row.Reason, at) || !strings.Contains(row.Reason, "you") {
		t.Errorf("the row for a pinned pair reads %q, want it to name the path it sits at and the operator who renews it", row.Reason)
	}
	if _, obtained := kept[certs.ProxyHandle("shop.example.com")]; !obtained {
		t.Errorf("the rows kept are %v, want shop.example.com named by the proxy handle: nothing on this box pins it, so the proxy obtained it and renews it", slices.Sorted(maps.Keys(kept)))
	}
	if _, wrong := kept[certs.ProxyHandle(pinned)]; wrong {
		t.Errorf("%s is named as a certificate this box's proxy obtained and renews, and a pinned pair covers it", certs.ProxyHandle(pinned))
	}
}

func TestReleasingAPreviewWildcardNamesTheRouteItTakesAndTheCatchAllItLeaves(t *testing.T) {
	t.Parallel()

	const wildcard = "*.preview.example.com"
	front := edgeOver(aMachine(), fake.NewKeyValues())
	removed, kept, err := front.PlanPreviewWildcardRemoval(wildcard)
	if err != nil {
		t.Fatal(err)
	}

	if removed.Action != edge.PlanDelete {
		t.Errorf("the removed group is actioned %q, want %q: rollingOut the wildcard takes down the route that serves it", removed.Action, edge.PlanDelete)
	}
	if len(removed.Changes) != 1 || removed.Changes[0].Name != wildcard {
		t.Fatalf("the removed group has %v, want the one route claiming %s", removed.Changes, wildcard)
	}
	if removed.Changes[0].Kind != switchboard.RouteKind || removed.Changes[0].Action != edge.PlanDelete {
		t.Errorf("the wildcard row is %q actioned %q, want a %q delete: a box records a wildcard as a claim on its own proxy and nothing else",
			removed.Changes[0].Kind, removed.Changes[0].Action, switchboard.RouteKind)
	}
	shared := front.PlanSharedPreviewRemoval()
	if kept.Action != shared.Action || kept.Reason != shared.Reason || len(kept.Changes) != 0 {
		t.Errorf("the kept group is %+v, want the shared catch-all removal %+v: the wildcard and the catch-all every unclaimed hostname falls through to are two rows, and folding them into one hides which of them the release touches", kept, shared)
	}
}

func TestTheSharedCatchAllIsAKeptRowThatSaysWhyItStays(t *testing.T) {
	t.Parallel()

	shared := edgeOver(aMachine(), fake.NewKeyValues()).PlanSharedPreviewRemoval()
	if shared.Action != edge.PlanKeep {
		t.Errorf("the shared catch-all is actioned %q, want %q: it is a bootstrap item and it answers for every project this box serves", shared.Action, edge.PlanKeep)
	}
	if len(shared.Changes) != 0 {
		t.Errorf("the shared catch-all has rows %v; a kept group that lists rows reads as a removal that spared them", shared.Changes)
	}
	if shared.Reason == "" {
		t.Error("the shared catch-all is kept for no stated reason, and a kept row a user cannot explain is one they cannot decide to remove by hand")
	}
	if shared.Name != edge.EdgeGroupName(edge.None) || shared.Kind != edge.EdgeGroupKind {
		t.Errorf("the shared catch-all is named %q of kind %q, want the origin's own group", shared.Name, shared.Kind)
	}
}

func reconciledOn(t *testing.T, m *fakeBox, named string) boxStack {
	t.Helper()

	front := edgeOver(m, fake.NewKeyValues())
	stackEdge, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Tier: environment.TierProduction, Slug: named,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(%s): %v", named, err)
	}
	stack := stackOn(front, stackEdge)
	return stack
}

func promoted(t *testing.T, stack boxStack, id, app, identity string) error {
	t.Helper()
	return stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: id, Ts: 1, Releases: map[string]string{app: identity},
	}}, progress.Discard())
}

func TestTwoProjectsRunningTheSameAppNameOnOneBoxAreReleasedSeparately(t *testing.T) {
	t.Parallel()

	m := aMachine()
	shop, blog := reconciledOn(t, m, "shop"), reconciledOn(t, m, "blog")
	staged(t, shop, "web", "b1", "shop-web-1111")
	staged(t, blog, "web", "b1", "blog-web-1111")

	if err := promoted(t, shop, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(shop): %v", err)
	}
	if err := promoted(t, blog, "p2", "web", "b1"); err != nil {
		t.Fatalf("Promote(blog): %v", err)
	}

	keys := map[string]bool{}
	for _, rollout := range m.rollouts {
		for _, app := range rollout.Apps {
			keys[app.Owner] = true
		}
	}
	if len(keys) != 2 {
		t.Fatalf("the two projects released under %v, want a route apiece: a route named by the app alone is one project's deploy stopping the other's live container", keys)
	}
}

func TestARollbackOntoAnImageAPruneRemovedIsServedFromItsArchive(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	if err := promoted(t, stack, "p2", "web", "b2"); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}
	m.swept[imageFor("web", "b1")] = true
	m.archived[slug+"/web "+imageFor("web", "b1")] = true
	m.started = nil

	err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p3", Ts: 3, Releases: map[string]string{"web": "b1"},
	}}, progress.Discard())

	if err != nil {
		t.Fatalf("a rollback onto a release whose image a prune removed and whose archive remains = %v, want it served from the archive", err)
	}
	if len(m.started) != 1 || m.started[0].Image != imageFor("web", "b1") {
		t.Errorf("the rollback started %+v, want shop-web-1111 on %s", m.started, imageFor("web", "b1"))
	}
}

func TestARollbackOntoASweptImageIsRefusedBeforeThePointerMoves(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	if err := promoted(t, stack, "p2", "web", "b2"); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}
	m.swept[imageFor("web", "b1")] = true
	m.calls = nil

	err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p3", Ts: 3, Releases: map[string]string{"web": "b1"},
	}}, progress.Discard())
	if err == nil {
		t.Fatal("a rollback onto an image this box has swept succeeded, and docker run would then reach for a registry")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Errorf("the rollback failed with %v, want a refusal naming what is missing rather than docker's own error", err)
	}
	if !strings.Contains(err.Error(), "Deploy again") {
		t.Errorf("the refusal reads %q and never says what to do instead", err)
	}
	if len(m.calls) != 0 {
		t.Errorf("the box was reached before the image was found gone: %v", m.calls)
	}

	entries, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(entries, func(entry router.HistoryEntry) bool { return entry.Active })
	if at < 0 {
		t.Fatalf("the ledger records no active promotion after a refused rollback (%v), and a pointer at nothing is not the release still serving", entries)
	}
	if entries[at].PromotionID != "p2" {
		t.Errorf("the pointer is at %s after a refused rollback, want the release still serving: the ensure runs before the cutover so a rollback that cannot serve leaves nothing moved", entries[at].PromotionID)
	}
}

func TestAPromotionPutsTheReleaseItServesAtTheHeadOfTheBoxsWindow(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	if err := promoted(t, stack, "p2", "web", "b2"); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}
	if err := promoted(t, stack, "p3", "web", "b1"); err != nil {
		t.Fatalf("Promote(rollback): %v", err)
	}

	if len(m.headed) == 0 || m.headed[len(m.headed)-1] != imageFor("web", "b1") {
		t.Errorf("the box's release window heads at %v, want %s: a rollback that leaves the window alone is swept off the box by the next deploy's reconcile while the ledger still offers it", m.headed, imageFor("web", "b1"))
	}
}

func TestAPromotionSaysItIsRestartingTheContainerBeforeItStartsIt(t *testing.T) {
	t.Parallel()

	_, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	heard := &fake.Log{}
	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1"},
	}}, heard); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !slices.Contains(heard.Lines(), "INFO Starting web's container shop-web-1111 again") {
		t.Errorf("the promotion reported %v and never named the container it started; a rollback provisions nothing, so this is the only row saying the box put a container back before the cutover", heard.Lines())
	}
}

func TestRemovingAPointerTakesTheRoutesItPointedAt(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(m.upstream) != 1 {
		t.Fatalf("the promotion routed %v, and this test needs a route to remove", m.upstream)
	}

	if _, err := removePointer(context.Background(), stack, "", progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if len(m.upstream) != 0 {
		t.Errorf("the routes left after the pointer was removed are %v; nothing offers those releases any more and the proxy still forwards to their containers", m.upstream)
	}
}

func TestRemovingAPointerTakesTheRouteOfAnAppTheLedgerNoLongerRemembers(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "worker", "b1", "shop-worker-1111")
	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1", "worker": "b1"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p2", "web", "b2"); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}
	staged(t, stack, "web", "b3", "shop-web-3333")
	if err := promoted(t, stack, "p3", "web", "b3"); err != nil {
		t.Fatalf("Promote(p3): %v", err)
	}
	if _, _, err := stack.Ledger().Prune(context.Background(), 1, ""); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	entries, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, dropped := entry.Releases["worker"]; dropped {
			t.Fatalf("the promotion that built worker is still retained (%v), and this test needs one the ledger has forgotten", entries)
		}
	}

	if _, err := removePointer(context.Background(), stack, "", progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if len(m.upstream) != 0 {
		t.Errorf("the routes left after the pointer was removed are %v; the ledger remembers only the promotions it retained, so an app dropped from the build set and aged out of that window keeps a route forwarding to a container this box removed. The proxy is what says which routes are live", m.upstream)
	}
}

func TestDestroyingAStackLeavesNoRouteOnTheBoxAtAll(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "worker", "b1", "shop-worker-1111")
	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1", "worker": "b1"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(m.upstream) != 0 {
		t.Errorf("a torn-down project leaves %v on this box's proxy, forwarding to containers the teardown removed", m.upstream)
	}
}

func TestABindNamingAnAppClaimsTheHostnameForThatAppAndTheSurfaceStillOwnsIt(t *testing.T) {
	t.Parallel()

	const hostname = "api.example.com"
	m, front, stack := reconciled(t)
	ctx := context.Background()
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname, App: "api"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	claims, err := m.Claims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := boxapi.Claim{Hostname: hostname, Owner: switchboard.Surface(slug, environment.TierProduction), Pointer: router.DefaultPointer, App: "api"}
	if len(claims) != 1 || claims[0] != want {
		t.Errorf("the box records %v, want %v: a project running two apps binds a hostname to one of them, and a claim that drops the app leaves the render unable to tell which route answers it", claims, want)
	}

	owner, err := front.DomainOwner(ctx, hostname)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if owner != switchboard.Surface(slug, environment.TierProduction) {
		t.Errorf("DomainOwner(%q) = %q, want the surface: the claim guard and the preview-entry check are both project-grained, so this answer stays surface-valued whatever app the hostname was declared under", hostname, owner)
	}
}

func TestTheRemovalPlanKeepsABoundHostnamesCertificateUnderTheProxysOwnHandle(t *testing.T) {
	t.Parallel()

	const bound = "shop.example.com"

	_, front, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: bound}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if _, err := removePointer(context.Background(), stack, "", progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}

	certificate := keptCertificate(t, must(front.PlanProjectRemoval(edge.ProjectScope{Slug: slug, Tier: environment.TierProduction, Hostnames: []string{bound}})))
	if certificate.Name != certs.ProxyHandle(bound) {
		t.Errorf("the plan keeps %q, want the proxy's own handle for %s: the row a teardown shows is the store entry it is declining to touch", certificate.Name, bound)
	}
	if certificate.Reason == "" {
		t.Errorf("%+v is kept with no reason, and a kept row is the whole of what tells the operator why bytes this teardown found are still there", certificate)
	}
}

func keptCertificate(t *testing.T, groups []edge.PlanGroup) edge.PlanChange {
	t.Helper()

	var certificates []edge.PlanChange
	for _, group := range groups {
		for _, change := range group.Changes {
			if change.Kind == switchboard.CertificateKind {
				certificates = append(certificates, change)
			}
		}
	}
	if len(certificates) != 1 {
		t.Fatalf("the removal plan has %d certificate rows, want the one behind the bound hostname: %v", len(certificates), groups)
	}
	if certificates[0].Action != edge.PlanKeep {
		t.Fatalf("the plan renders %+v, want it kept: ocel placed no key in the proxy's store, so it removes none", certificates[0])
	}
	return certificates[0]
}

func TestAPromotionPassesTheNamesItsDeployResolvedSoTheBoxCanRefuseToServeNone(t *testing.T) {
	t.Parallel()

	m, _, stack := reconciled(t)
	if err := stack.Ledger().PutStaged(context.Background(), router.ReleaseRecord{
		App:          "web",
		Release:      "b1",
		RootFunction: "/",
		Image:        imageFor("web", "b1"),
		Physical:     "shop-web-1111",
		HealthPath:   "/healthz",
		Variables:    []router.VariableRecord{{Key: "API_TOKEN"}, {Key: "DATABASE_URL"}, {Key: "orders"}},
	}); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "b1"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(m.started) != 1 {
		t.Fatalf("the promotion started %d containers, want the one the record names", len(m.started))
	}
	spec := m.started[0]
	if spec.Resolved {
		t.Error("the promotion says it resolved the app's values, and it resolved none: a container running under the values a since-changed deploy handed it would then read as replaceable")
	}
	if !slices.Equal(spec.Declared, []string{"API_TOKEN", "DATABASE_URL", "orders"}) {
		t.Errorf("the promotion names %v of what the record says web was handed, and a box that is told none of them puts the app back with an empty environment rather than refusing", spec.Declared)
	}
	if spec.HealthPath != "/healthz" {
		t.Errorf("the promotion passes the health path %q, want the record's: a container the box re-creates is handed the path its runtime lets probes through on", spec.HealthPath)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
