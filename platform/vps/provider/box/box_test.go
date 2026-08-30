package box_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

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
	upstream map[string]string
	calls    []string
	releases []host.Release
	stood    []host.Container
	refuse   error
}

func aMachine() *machine { return &machine{upstream: map[string]string{}} }

func (m *machine) Address(context.Context) (string, error) { return address, nil }

func (m *machine) StandUp(_ context.Context, spec host.Container) error {
	m.calls = append(m.calls, "stand-up "+spec.Name)
	m.stood = append(m.stood, spec)
	return m.refuse
}

func (m *machine) Serving(_ context.Context, app string) (string, error) {
	m.calls = append(m.calls, "serving "+app)
	return m.upstream[app], nil
}

func (m *machine) Release(_ context.Context, rel host.Release, _ providerkit.Reporter) error {
	m.calls = append(m.calls, "release "+rel.App+" onto "+rel.Target)
	m.releases = append(m.releases, rel)
	m.upstream[rel.App] = rel.Target
	return m.refuse
}

func (m *machine) Claims(context.Context) ([]host.HostClaim, error) {
	return slices.Clone(m.claims), nil
}

func (m *machine) ClaimHost(_ context.Context, claim host.HostClaim) error {
	m.calls = append(m.calls, "claim "+claim.Hostname)
	m.claims = host.Claiming(m.claims, claim)
	return nil
}

func (m *machine) DisclaimHost(_ context.Context, hostname string) error {
	m.calls = append(m.calls, "disclaim "+hostname)
	m.claims = host.Disclaiming(m.claims, hostname)
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
		Image:      "ghcr.io/acme/" + app + ":" + identity,
		Physical:   physical,
		HealthPath: "/healthz",
	}); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
}

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

	want := []string{"stand-up shop-web-1111", "serving web", "release web onto shop-web-1111:" + host.AppPort}
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

	want := []string{"stand-up shop-web-1111", "serving web", "release web onto shop-web-1111:" + host.AppPort}
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
	if !slices.Contains(kinds, box.RouteKind) || !slices.Contains(kinds, box.CertificateKind) {
		t.Errorf("the removal rows are %v, want the routes claimed and the certificates held", kinds)
	}
}
