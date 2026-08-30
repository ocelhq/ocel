package box_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/edgeconformance"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	address  = "203.0.113.10"
	sshScope = "ada@ocelbox"
	slug     = "shop"
)

type machine struct {
	claims   []host.HostClaim
	upstream map[host.RouteKey]string
	swept    map[string]bool
	calls    []string
	releases []host.Release
	stood    []host.Container
	headed   []string
	refuse   error
}

func aMachine() *machine {
	return &machine{upstream: map[host.RouteKey]string{}, swept: map[string]bool{}}
}

func (m *machine) Address(context.Context) (string, error) { return address, nil }

func (m *machine) HoldsImage(_ context.Context, coordinate string) (bool, error) {
	return !m.swept[coordinate], nil
}

func (m *machine) StandUp(_ context.Context, spec host.Container) error {
	m.calls = append(m.calls, "stand-up "+spec.Name)
	m.stood = append(m.stood, spec)
	return m.refuse
}

func (m *machine) Promote(_ context.Context, _ providerkit.Class, app, coordinate string) error {
	m.calls = append(m.calls, "head "+app+" at "+coordinate)
	m.headed = append(m.headed, coordinate)
	return m.refuse
}

func (m *machine) Serving(_ context.Context, key host.RouteKey) (string, error) {
	m.calls = append(m.calls, "serving "+key.App)
	return m.upstream[key], nil
}

func (m *machine) Release(_ context.Context, rel host.Release, _ providerkit.Reporter) error {
	m.calls = append(m.calls, "release "+rel.App+" onto "+rel.Target)
	m.releases = append(m.releases, rel)
	m.upstream[rel.RouteKey] = rel.Target
	return m.refuse
}

func (m *machine) UnroutePointer(_ context.Context, owner, pointer string) error {
	m.calls = append(m.calls, "unroute "+owner+"/"+pointer)
	maps.DeleteFunc(m.upstream, func(key host.RouteKey, _ string) bool {
		return key.Owner == owner && key.Pointer == pointer
	})
	return nil
}

func (m *machine) UnrouteSurface(_ context.Context, owner string) error {
	m.calls = append(m.calls, "unroute "+owner)
	maps.DeleteFunc(m.upstream, func(key host.RouteKey, _ string) bool { return key.Owner == owner })
	return nil
}

func (m *machine) Claims(context.Context) ([]host.HostClaim, error) {
	return slices.Clone(m.claims), nil
}

func (m *machine) ClaimHost(_ context.Context, claim host.HostClaim) error {
	m.calls = append(m.calls, "claim "+claim.Hostname)
	taken, err := host.Claiming(m.claims, claim)
	if err != nil {
		return err
	}
	m.claims = taken
	return nil
}

func (m *machine) DisclaimHost(_ context.Context, hostname, owner string) error {
	m.calls = append(m.calls, "disclaim "+hostname)
	m.claims = host.Disclaiming(m.claims, hostname, owner)
	return nil
}

func TestTheBoxEdge(t *testing.T) {
	edgeconformance.Run(t, edgeconformance.Suite{
		Hostname: "shop.example.com",
		New: func(*testing.T) (edge.Edge, edge.StackSpec) {
			return box.New(aMachine(), fake.NewRecords(), sshScope),
				edge.StackSpec{Version: "test", Class: edge.ClassProduction, Slug: slug}
		},
	})
}

func standing(t *testing.T) (*machine, *box.Edge, edge.EdgeStack) {
	t.Helper()

	stood := aMachine()
	front := box.New(stood, fake.NewRecords(), sshScope)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassProduction, Slug: slug,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return stood, front, stack
}

func staged(t *testing.T, stack edge.EdgeStack, app, identity, physical string) {
	t.Helper()

	if err := stack.Ledger().PutStaged(context.Background(), edge.DeploymentRecord{
		App:        app,
		Identity:   identity,
		Entry:      "/",
		Image:      imageFor(app, identity),
		Physical:   physical,
		HealthPath: "/healthz",
	}); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
}

func imageFor(app, identity string) string { return "ghcr.io/acme/" + app + ":" + identity }

func TestTheEdgeAnswersTheFactsABoxCanStandBehind(t *testing.T) {
	t.Parallel()

	front := box.New(aMachine(), fake.NewRecords(), sshScope)
	facts := front.Facts()
	if facts.RunsCode || facts.SignsOriginForwards || facts.InvalidatesByCacheTag {
		t.Errorf("Facts() = %+v; a box runs the container and nothing in front of it, its origin is one hop on a private network that verifies no signature, and there is no edge cache to tag", facts)
	}
	if facts.ServesUnbound {
		t.Error("Facts().ServesUnbound = true; a box answers only the hostnames something on it claims, and DNS for one points at the box's own address")
	}
	if facts.CredentialScope != sshScope {
		t.Errorf("Facts().CredentialScope = %q, want the ssh destination %q it is reached at", facts.CredentialScope, sshScope)
	}
	if !slices.Equal(front.Supported(), []edge.Need{edge.NeedStreaming}) {
		t.Errorf("Supported() = %v, want streaming alone", front.Supported())
	}
}

func TestTheFlipCarriesNoPropagationNoteToPrint(t *testing.T) {
	t.Parallel()

	bound := box.New(aMachine(), fake.NewRecords(), sshScope).FlipBound()
	if bound.Typical > 0 {
		t.Errorf("FlipBound() = %+v, and a bound above zero is rendered to the user as a propagation note; when the flip call returns on a box the gate has passed, the config is loaded and the retired upstream has drained, so there is no window to advertise", bound)
	}
	if bound.Published {
		t.Errorf("FlipBound() = %+v, which publishes a bound it declares instant", bound)
	}
}

func TestBootstrappingTheEdgeTouchesTheBoxNotAtAll(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	front := box.New(stood, fake.NewRecords(), sshScope)
	out, err := front.Bootstrap(context.Background(), edge.ClassProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if out.Trust != edge.TrustInternal {
		t.Errorf("Bootstrap trust = %q, want %q: the proxy and everything it stands on are the box bootstrap's items", out.Trust, edge.TrustInternal)
	}
	if err := front.Teardown(context.Background(), edge.ClassProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(stood.calls) != 0 {
		t.Errorf("bootstrapping and tearing the edge down reached the box (%v); the proxy, its baseline, its helper and its data directory are bootstrap items and not edge methods", stood.calls)
	}
}

func TestPromoteEnsuresTheContainerIsRunningBeforeItFlips(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")

	if err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"},
	}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	want := []string{"stand-up shop-web-1111", "head web at " + imageFor("web", "b1"), "serving web", "release web onto shop-web-1111:" + host.AppPort}
	if !slices.Equal(stood.calls, want) {
		t.Fatalf("Promote drove the box as %v, want %v: it makes the promotion's containers running and only then flips", stood.calls, want)
	}
	if stood.releases[0].HealthPath != "/healthz" {
		t.Errorf("the release is gated on %q, want the path the record names: up is a 2xx on the path the wire named", stood.releases[0].HealthPath)
	}
	if stood.releases[0].Retire != "" {
		t.Errorf("the first release of an app retires %q, want nothing: there is no previous container to drain", stood.releases[0].Retire)
	}
}

func TestARollbackStandsThePreviousContainerBackUpAndFlipsOntoIt(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")

	ctx := context.Background()
	for _, promotion := range []edge.Promotion{
		{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"}},
		{PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": "b2"}},
	} {
		if err := stack.Promote(ctx, promotion, "", edge.DiscardReporter()); err != nil {
			t.Fatalf("Promote(%s): %v", promotion.PromotionID, err)
		}
	}
	stood.calls = nil

	if err := stack.Promote(ctx, edge.Promotion{
		PromotionID: "p3", Ts: 3, Builds: map[string]string{"web": "b1"},
	}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(rollback): %v", err)
	}

	want := []string{"stand-up shop-web-1111", "head web at " + imageFor("web", "b1"), "serving web", "release web onto shop-web-1111:" + host.AppPort}
	if !slices.Equal(stood.calls, want) {
		t.Fatalf("a rollback drove the box as %v, want %v: nothing provisions on this path, so re-pointing at a release that is not running is a ledger edit and not a restored site", stood.calls, want)
	}
	last := stood.releases[len(stood.releases)-1]
	if last.Retire != "shop-web-2222:"+host.AppPort {
		t.Errorf("the rollback retires %q, want the container it is rolling off", last.Retire)
	}
}

type staleAt struct {
	providerkit.RecordStore
	at string
}

func (s staleAt) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	if slices.Contains(record.Name, s.at) {
		return "", providerkit.ErrStale
	}
	return s.RecordStore.Write(ctx, record)
}

func TestADeployThatLostTheRaceForThePointerNeverReachesTheProxy(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	front := box.New(stood, staleAt{RecordStore: fake.NewRecords(), at: "pointers"}, sshScope)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassProduction, Slug: slug,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	staged(t, stack, "web", "b1", "shop-web-1111")

	err = stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"},
	}, "", edge.DiscardReporter())
	if err == nil {
		t.Fatal("Promote succeeded while the pointer moved under it")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Errorf("Promote refused with %v, want %s: the deploy that lost the race is told another one moved the pointer", err, providerkit.CodeBusy)
	}
	if len(stood.calls) != 0 {
		t.Errorf("a deploy that lost the pointer still reached the box (%v); the flip rewrites the whole box's proxy configuration, so a deploy that lost the race would retire a live app's route on its way past", stood.calls)
	}
}

func TestAnAppWithNoContainerOnThisBoxFlipsNothing(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	staged(t, stack, "web", "b1", "")

	if err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"},
	}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(stood.calls) != 0 {
		t.Errorf("a promotion naming no container on this box drove the proxy (%v); there is nothing here for it to put in front", stood.calls)
	}
}

func TestARecordNamingAContainerAndNoHealthPathIsRefusedRatherThanGatedOnAGuess(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	if err := stack.Ledger().PutStaged(context.Background(), edge.DeploymentRecord{
		App: "web", Identity: "b1", Image: "ghcr.io/acme/web:b1", Physical: "shop-web-1111",
	}); err != nil {
		t.Fatal(err)
	}

	err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"},
	}, "", edge.DiscardReporter())
	if err == nil {
		t.Fatal("Promote gated a container on a health path nothing named")
	}
	if !strings.Contains(err.Error(), "health path") {
		t.Errorf("Promote refused with %q, want it to name the health path it has no value for", err)
	}
	if len(stood.calls) != 0 {
		t.Errorf("the box was reached before the record was found unusable: %v", stood.calls)
	}
}

func TestABoundHostnameIsClaimedOnTheProxyAndPointedAtTheBoxItself(t *testing.T) {
	t.Parallel()

	const hostname = "shop.example.com"
	stood, front, stack := standing(t)
	ctx := context.Background()
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: hostname}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	owner, err := front.DomainOwner(ctx, hostname)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if owner != box.Surface(slug, edge.ClassProduction) {
		t.Errorf("DomainOwner(%q) = %q, want the surface the binding claimed it for", hostname, owner)
	}
	records, err := edge.RecordsFor(edge.TargetFor(front, stack.State()), stack.State().Bound)
	if err != nil {
		t.Fatalf("RecordsFor: %v", err)
	}
	want := edge.Record{Name: hostname, Type: edge.RecordTypeA, Value: address}
	if len(records) != 1 || records[0] != want {
		t.Errorf("records = %v, want %v: a box answers on an address rather than a name, so the record that points at it is an A record", records, want)
	}
	if !slices.Contains(stood.calls, "claim "+hostname) {
		t.Errorf("the binding never claimed %q on the proxy (%v), and nothing on the box would then say which project answers it", hostname, stood.calls)
	}
}

func TestAPreviewWildcardIsRefusedByNameRatherThanServedWrong(t *testing.T) {
	t.Parallel()

	front := box.New(aMachine(), fake.NewRecords(), sshScope)
	if _, err := front.ReconcilePreviewWildcard(context.Background(), edge.PreviewWildcardSpec{BaseDomain: "preview.example.com"}); err == nil {
		t.Error("ReconcilePreviewWildcard answered a front where a box raises no wildcard")
	}
	if err := front.DestroyPreviewWildcard(context.Background(), "preview.example.com"); err == nil {
		t.Error("DestroyPreviewWildcard took down a wildcard a box never raised")
	}
}

func TestTheRemovalPlanNamesTheEdgesRowsAndNotTheContainersReleasesOwn(t *testing.T) {
	t.Parallel()

	front := box.New(aMachine(), fake.NewRecords(), sshScope)
	groups := front.ProjectRemovals(edge.ProjectScope{
		Slug: slug, Class: edge.ClassProduction, Hostnames: []string{"shop.example.com"}, Front: address,
	})
	if len(groups) != 1 {
		t.Fatalf("ProjectRemovals = %d groups, want one", len(groups))
	}
	for _, change := range groups[0].Changes {
		if change.Kind == host.KindContainer {
			t.Errorf("the edge's removal plan names the container %q, which the release surface already names: a teardown plan that names it twice offers to remove it twice", change.Name)
		}
	}
	kinds := make([]string, 0, len(groups[0].Changes))
	for _, change := range groups[0].Changes {
		kinds = append(kinds, change.Kind)
	}
	if !slices.Contains(kinds, box.RouteKind) {
		t.Errorf("the removal rows are %v, want the routes this project claimed", kinds)
	}
	for _, change := range groups[0].Changes {
		if strings.Contains(change.Reason, "certificate") {
			t.Errorf("the removal plan offers to delete %q: this box's proxy listens on :80 alone, which disables automatic https, so nothing here ever obtained a certificate to delete", change.Reason)
		}
	}
}

func standingOn(t *testing.T, stood *machine, named string) edge.EdgeStack {
	t.Helper()

	front := box.New(stood, fake.NewRecords(), sshScope)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassProduction, Slug: named,
	}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(%s): %v", named, err)
	}
	return stack
}

func promoted(t *testing.T, stack edge.EdgeStack, id, app, identity string) error {
	t.Helper()
	return stack.Promote(context.Background(), edge.Promotion{
		PromotionID: id, Ts: 1, Builds: map[string]string{app: identity},
	}, "", edge.DiscardReporter())
}

func TestTwoProjectsRunningTheSameAppNameOnOneBoxAreReleasedSeparately(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	shop, blog := standingOn(t, stood, "shop"), standingOn(t, stood, "blog")
	staged(t, shop, "web", "b1", "shop-web-1111")
	staged(t, blog, "web", "b1", "blog-web-1111")

	if err := promoted(t, shop, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(shop): %v", err)
	}
	if err := promoted(t, blog, "p2", "web", "b1"); err != nil {
		t.Fatalf("Promote(blog): %v", err)
	}

	keys := map[string]bool{}
	for _, rel := range stood.releases {
		if keys[rel.Owner] {
			continue
		}
		keys[rel.Owner] = true
	}
	if len(keys) != 2 {
		t.Fatalf("the two projects released under %v, want a route apiece: a route named by the app alone is one project's deploy stopping the other's live container", keys)
	}
	if retiring := stood.releases[1].Retire; retiring != "" {
		t.Errorf("the second project's first deploy retires %q, want nothing: that upstream belongs to the other project and stopping it takes a live site down", retiring)
	}
}

func TestARollbackOntoASweptImageIsRefusedBeforeThePointerMoves(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	if err := promoted(t, stack, "p2", "web", "b2"); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}
	stood.swept[imageFor("web", "b1")] = true
	stood.calls = nil

	err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p3", Ts: 3, Builds: map[string]string{"web": "b1"},
	}, "", edge.DiscardReporter())
	if err == nil {
		t.Fatal("a rollback onto an image this box has swept succeeded, and docker run would then reach for a registry")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Errorf("the rollback failed with %v, want a refusal naming what is missing rather than docker's own error", err)
	}
	if !strings.Contains(err.Error(), "deploy again") {
		t.Errorf("the refusal reads %q and never says what to do instead", err)
	}
	if len(stood.calls) != 0 {
		t.Errorf("the box was reached before the image was found gone: %v", stood.calls)
	}

	entries, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(entries, func(entry edge.HistoryEntry) bool { return entry.Active })
	if at < 0 {
		t.Fatalf("the ledger holds no active promotion after a refused rollback (%v), and a pointer standing at nothing is not the release still serving", entries)
	}
	if entries[at].PromotionID != "p2" {
		t.Errorf("the pointer stands at %s after a refused rollback, want the release still serving: the ensure runs before the flip so a rollback that cannot serve leaves nothing moved", entries[at].PromotionID)
	}
}

func TestAPromotionPutsTheReleaseItServesAtTheHeadOfTheBoxsWindow(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
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

	if len(stood.headed) == 0 || stood.headed[len(stood.headed)-1] != imageFor("web", "b1") {
		t.Errorf("the box's release window heads at %v, want %s: a rollback that leaves the window alone is swept off the box by the next deploy's reconcile while the ledger still offers it", stood.headed, imageFor("web", "b1"))
	}
}

type reported struct{ lines []string }

func (r *reported) Say(message string)    { r.lines = append(r.lines, message) }
func (r *reported) Detail(message string) { r.lines = append(r.lines, message) }

func (r *reported) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

func TestAPromotionSaysItIsStandingTheContainerBackUpBeforeItStandsIt(t *testing.T) {
	t.Parallel()

	_, _, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	heard := &reported{}
	if err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"},
	}, "", heard); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !slices.ContainsFunc(heard.lines, func(line string) bool { return strings.Contains(line, "shop-web-1111") }) {
		t.Errorf("the promotion reported %v and never named the container it stood up; a rollback provisions nothing, so this is the only row saying the box put a container back before the flip", heard.lines)
	}
}

func TestRemovingAPointerTakesTheRoutesItPointedAt(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(stood.upstream) != 1 {
		t.Fatalf("the promotion routed %v, and this test needs a route to remove", stood.upstream)
	}

	if _, err := stack.RemovePointer(context.Background(), ""); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if len(stood.upstream) != 0 {
		t.Errorf("the routes left after the pointer was removed are %v; nothing offers those releases any more and the proxy still forwards to their containers", stood.upstream)
	}
}

func TestRemovingAPointerTakesTheRouteOfAnAppTheLedgerNoLongerRemembers(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "worker", "b1", "shop-worker-1111")
	if err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1", "worker": "b1"},
	}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	staged(t, stack, "web", "b2", "shop-web-2222")
	if err := promoted(t, stack, "p2", "web", "b2"); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}
	if _, err := stack.Ledger().Prune(context.Background(), 1, ""); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	entries, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, dropped := entry.Builds["worker"]; dropped {
			t.Fatalf("the promotion that built worker is still retained (%v), and this test needs one the ledger has forgotten", entries)
		}
	}

	if _, err := stack.RemovePointer(context.Background(), ""); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if len(stood.upstream) != 0 {
		t.Errorf("the routes left after the pointer was removed are %v; the ledger remembers only the promotions it retained, so an app dropped from the build set and aged out of that window keeps a route forwarding to a container this box removed. The proxy is what says which routes are live", stood.upstream)
	}
}

func TestDestroyingAStackLeavesNoRouteOnTheBoxAtAll(t *testing.T) {
	t.Parallel()

	stood, _, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	staged(t, stack, "worker", "b1", "shop-worker-1111")
	if err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1", "worker": "b1"},
	}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(stood.upstream) != 0 {
		t.Errorf("a torn-down project leaves %v on this box's proxy, forwarding to containers the teardown removed", stood.upstream)
	}
}
