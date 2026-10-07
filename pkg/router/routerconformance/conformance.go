package routerconformance

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const App = "web"

const raceChecks = 16

type Fixture struct {
	Router              router.Router
	Spec                router.StackSpec
	Prior               router.StackState
	Serving             func(pointer string) string
	FailNextPointerMove func(err error)
}

type Suite struct {
	New         func(t *testing.T) Fixture
	Previews    func(t *testing.T) Fixture
	Pointer     string
	Hostname    string
	PreviewBase string
	Record      func(app, release string) router.ReleaseRecord
	Tunnel      edge.Kind
}

var errDisplaced = errors.New("conformance: another promotion displaced this one while it moved")

var errDataPlane = errors.New("conformance: the data plane refused the write")

func Run(t *testing.T, suite Suite) {
	t.Helper()

	if suite.Hostname == "" {
		t.Fatal("the suite names no hostname for the claim checks")
	}
	pointer := router.ResolvePointer(suite.Pointer)
	record := suite.Record
	if record == nil {
		record = functionRecord
	}

	t.Run("the propagation is one a caller can wait out", func(t *testing.T) {
		facts := suite.New(t).Router.Facts()
		bound := facts.Propagation
		if bound.Typical < 0 {
			t.Errorf("Facts().Propagation.Typical = %v, want a duration a caller can wait out", bound.Typical)
		}
		if bound.Typical == 0 && bound.Published {
			t.Error("Facts().Propagation publishes a propagation it declares instant; Published is read only when Typical > 0")
		}
		if facts.CachesRecords && bound.Typical == 0 {
			t.Error("Facts().Propagation.Typical = 0 on a router whose Facts().CachesRecords is true; a router that serves a promotion from a cached record keeps serving the old one until the cache lapses, and a caller waiting on the pointer move needs that propagation")
		}
	})

	t.Run("a router reaches at least one kind of compute", func(t *testing.T) {
		facts := suite.New(t).Router.Facts()
		if !facts.ReachesFunctions && !facts.ReachesContainers {
			t.Error("Facts() reaches neither functions nor containers, so no release could ever be served through this router")
		}
	})

	t.Run("a pointer move serves the release it names on its pointer", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		movePointer(t, stack, pointer, "conformance-b1", record(App, "b1"))
		if served := fixture.Serving(pointer); served != "b1" {
			t.Fatalf("%s serves %q after a pointer move onto b1, want b1", pointer, served)
		}
		movePointer(t, stack, pointer, "conformance-b2", record(App, "b2"))
		if served := fixture.Serving(pointer); served != "b2" {
			t.Errorf("%s serves %q after a pointer move onto b2, want b2", pointer, served)
		}
	})

	t.Run("a pointer move whose promotion is no longer active moves nothing and is unserved", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		movePointer(t, stack, pointer, "conformance-b1", record(App, "b1"))

		displaced := newPointerMove("conformance-b2", record(App, "b2"))
		displaced.Pointer = pointer
		displaced.StillActive = func(context.Context) error { return errDisplaced }
		err := stack.MovePointer(context.Background(), displaced, progress.Discard())
		if !errors.Is(err, errDisplaced) {
			t.Fatalf("MovePointer with a StillActive that refuses = %v, want that refusal", err)
		}
		var unserved router.Unserved
		if !errors.As(err, &unserved) {
			t.Errorf("MovePointer with a StillActive that refuses = %v, want it reported as router.Unserved: nothing moved, so the caller may unwind", err)
		}
		if served := fixture.Serving(pointer); served != "b1" {
			t.Errorf("%s serves %q after a pointer move StillActive refused, want the b1 it served before", pointer, served)
		}
	})

	t.Run("two promotes racing on one pointer leave it serving the release the ledger names", func(t *testing.T) {
		for check := 1; check <= raceChecks; check++ {
			reached := false
			t.Run(fmt.Sprintf("the racing promote lands after StillActive answers call %d", check), func(t *testing.T) {
				reached = racesAfterCheck(t, suite.New(t), pointer, record, check)
			})
			if !reached {
				return
			}
		}
		t.Fatalf("MovePointer asked StillActive more than %d times, and the suite races a promote after each one", raceChecks)
	})

	t.Run("a pointer move the data plane refuses is unserved and leaves the release it served", func(t *testing.T) {
		fixture := suite.New(t)
		if fixture.FailNextPointerMove == nil {
			t.Fatal("the fixture cannot make the data plane refuse a pointer move, and every router must say when a pointer move did not move the pointer")
		}
		stack := reconciled(t, fixture)
		movePointer(t, stack, pointer, "conformance-b1", record(App, "b1"))

		refused := newPointerMove("conformance-b2", record(App, "b2"))
		refused.Pointer = pointer
		fixture.FailNextPointerMove(errDataPlane)
		err := stack.MovePointer(context.Background(), refused, progress.Discard())
		var unserved router.Unserved
		if !errors.As(err, &unserved) {
			t.Fatalf("MovePointer the data plane refused = %v, want router.Unserved", err)
		}
		if served := fixture.Serving(pointer); served != "b1" {
			t.Errorf("%s serves %q after a pointer move the data plane refused, want the b1 it served before", pointer, served)
		}
	})

	t.Run("removing a pointer stops serving it on a router whose facts say so, and leaves it serving on one torn down with its compute", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		movePointer(t, stack, pointer, "conformance-b1", record(App, "b1"))

		removesPointer(t, stack, pointer)
		served := fixture.Serving(pointer)
		if fixture.Router.Facts().StopsServingRemovedPointers {
			if served != "" {
				t.Errorf("%s serves %q after it was removed, want nothing: Facts().StopsServingRemovedPointers is true", pointer, served)
			}
		} else if served != "b1" {
			t.Errorf("%s serves %q after it was removed, want the b1 it served: Facts().StopsServingRemovedPointers is false, so what serves it goes with its compute", pointer, served)
		}
		removesPointer(t, stack, pointer)
	})

	t.Run("a router that answers hostnames takes a claim, and one that does not refuses it", func(t *testing.T) {
		ctx := context.Background()
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		claim := router.Claim{Hostname: suite.Hostname, App: App}
		if !fixture.Router.Facts().AnswersHostnames {
			_, err := stack.Claim(ctx, claim)
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Errorf("Claim on a router that answers no hostname = %v, want a refusal with code %s", err, refusal.CodeInvalid)
			}
			return
		}
		first, err := stack.Claim(ctx, claim)
		if err != nil {
			t.Fatalf("Claim(%q): %v", suite.Hostname, err)
		}
		again, err := stack.Claim(ctx, claim)
		if err != nil {
			t.Fatalf("Claim(%q) again: %v", suite.Hostname, err)
		}
		if again != first {
			t.Errorf("Claim(%q) again names origin %+v, want the %+v the first claim named: an edge forwarding to that origin keeps forwarding there across deploys", suite.Hostname, again, first)
		}
		for range 2 {
			if err := stack.Disclaim(ctx, suite.Hostname); err != nil {
				t.Fatalf("Disclaim(%q): %v", suite.Hostname, err)
			}
		}
	})

	t.Run("a reconciled stack reopens onto the data plane it moved", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		movePointer(t, stack, pointer, "conformance-b1", record(App, "b1"))

		reopened, err := fixture.Router.Open(stack.State())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		movePointer(t, reopened, pointer, "conformance-b2", record(App, "b2"))
		if served := fixture.Serving(pointer); served != "b2" {
			t.Errorf("%s serves %q after the reopened stack moved onto b2, want b2", pointer, served)
		}
	})

	t.Run("state survives the seam it is persisted through", func(t *testing.T) {
		fixture := suite.New(t)
		stack := reconciled(t, fixture)
		movePointer(t, stack, pointer, "conformance-b1", record(App, "b1"))

		reopened, err := fixture.Router.Open(roundTrip(t, stack.State()))
		if err != nil {
			t.Fatalf("Open a persisted state: %v", err)
		}
		movePointer(t, reopened, pointer, "conformance-b2", record(App, "b2"))
		if served := fixture.Serving(pointer); served != "b2" {
			t.Errorf("%s serves %q after a stack reopened from its persisted state moved onto b2, want b2", pointer, served)
		}
	})

	runPreviews(t, suite, record)
	runOrigin(t, suite)
}

const conformanceCertificate = "conformance-certificate"

func runOrigin(t *testing.T, suite Suite) {
	t.Run("a router an edge forwards to answers a claim trusting the CA of the edge's client certificate with the certificate the edge issued", func(t *testing.T) {
		ctx := context.Background()
		fixture := suite.New(t)
		origin := fixture.Router.Hooks().Origin
		if origin == nil {
			t.Skip("no edge forwards to this router as its origin")
		}
		stack := reconciled(t, fixture)
		claim := router.Claim{
			Hostname: suite.Hostname, App: App, Certificate: conformanceCertificate,
			ClientCAs:         []string{mintClientCA(t)},
			OriginCertificate: mintOriginCertificate(t, suite.Hostname),
		}
		first, err := stack.Claim(ctx, claim)
		if err != nil {
			t.Fatalf("Claim(%q) trusting a client CA: %v", suite.Hostname, err)
		}
		if first.Address == "" || !first.Certified {
			t.Errorf("Claim(%q) with an origin certificate names origin %+v, want an address that answers with a certificate covering it", suite.Hostname, first)
		}
		claim.OriginCertificate = edge.OriginCertificate{}
		again, err := stack.Claim(ctx, claim)
		if err != nil {
			t.Fatalf("Claim(%q) again: %v", suite.Hostname, err)
		}
		if again != first {
			t.Errorf("Claim(%q) again with no origin certificate names %+v, want the %+v it named: the origin keeps answering with the certificate it holds", suite.Hostname, again, first)
		}
		requirePlan(t, "PlanProjectRemoval", origin.PlanProjectRemoval(edge.ProjectScope{
			Slug: fixture.Spec.Slug, Tier: fixture.Spec.Tier, Hostnames: []string{suite.Hostname},
		}))
		for range 2 {
			if err := stack.Disclaim(ctx, suite.Hostname); err != nil {
				t.Fatalf("Disclaim(%q): %v", suite.Hostname, err)
			}
		}
	})

	t.Run("a router an edge reaches through a tunnel answers a claim through it with the tunnel, and gives it back", func(t *testing.T) {
		if suite.Tunnel == edge.None {
			t.Skip("no edge reaches this router through a tunnel")
		}
		ctx := context.Background()
		stack := reconciled(t, suite.New(t))
		claim := router.Claim{Hostname: suite.Hostname, App: App, Certificate: conformanceCertificate, Tunnel: suite.Tunnel}
		first, err := stack.Claim(ctx, claim)
		if err != nil {
			t.Fatalf("Claim(%q) through a tunnel: %v", suite.Hostname, err)
		}
		if first.Address == "" || !first.Tunneled || !first.Certified {
			t.Errorf("Claim(%q) through a tunnel names origin %+v, want the tunnel's address, tunneled: the edge reaches the origin through nothing else, and checks no certificate it issued", suite.Hostname, first)
		}
		again, err := stack.Claim(ctx, claim)
		if err != nil {
			t.Fatalf("Claim(%q) through a tunnel again: %v", suite.Hostname, err)
		}
		if again != first {
			t.Errorf("Claim(%q) through a tunnel again names %+v, want the %+v it named: one tunnel reaches the origin across deploys", suite.Hostname, again, first)
		}
		for range 2 {
			if err := stack.Disclaim(ctx, suite.Hostname); err != nil {
				t.Fatalf("Disclaim(%q): %v", suite.Hostname, err)
			}
		}
	})

	t.Run("a router an edge forwards previews to claims the preview entry and gives it back", func(t *testing.T) {
		ctx := context.Background()
		fixture := suite.New(t)
		origin := fixture.Router.Hooks().Origin
		switch {
		case origin == nil:
			t.Skip("no edge forwards to this router as its origin")
		case suite.PreviewBase == "":
			t.Skip("the suite names no preview base domain")
		}
		wildcard := edge.PreviewWildcard(suite.PreviewBase)
		if _, err := origin.ClaimPreviewEntry(ctx, router.Claim{Hostname: suite.Hostname, Certificate: conformanceCertificate}); err == nil {
			t.Errorf("ClaimPreviewEntry(%q) = nil, want it refused: a preview entry is a wildcard", suite.Hostname)
		}
		claim := router.Claim{
			Hostname: wildcard, Certificate: conformanceCertificate,
			ClientCAs:         []string{mintClientCA(t)},
			OriginCertificate: mintOriginCertificate(t, wildcard),
		}
		first, err := origin.ClaimPreviewEntry(ctx, claim)
		if err != nil {
			t.Fatalf("ClaimPreviewEntry(%q): %v", wildcard, err)
		}
		if first.Address == "" || !first.Certified {
			t.Errorf("ClaimPreviewEntry(%q) names origin %+v, want an address that answers with a certificate covering it", wildcard, first)
		}
		again, err := origin.ClaimPreviewEntry(ctx, claim)
		if err != nil {
			t.Fatalf("ClaimPreviewEntry(%q) again: %v", wildcard, err)
		}
		if again != first {
			t.Errorf("ClaimPreviewEntry(%q) again names %+v, want the %+v it named", wildcard, again, first)
		}
		requirePlan(t, "PlanPreviewEntryRemoval", origin.PlanPreviewEntryRemoval(wildcard))
		for range 2 {
			if err := origin.DisclaimPreviewEntry(ctx, suite.PreviewBase); err != nil {
				t.Fatalf("DisclaimPreviewEntry(%q): %v", suite.PreviewBase, err)
			}
		}
	})
}

func requirePlan(t *testing.T, named string, groups []edge.PlanGroup) {
	t.Helper()
	if len(groups) == 0 {
		t.Errorf("%s = none, want what the router takes down: a plan names everything the removal deletes", named)
	}
	for _, group := range groups {
		if group.Kind == "" || group.Name == "" {
			t.Errorf("%s names group %+v with no kind or name", named, group)
		}
		for _, change := range group.Changes {
			if change.Kind == "" || change.Name == "" {
				t.Errorf("%s names change %+v with no kind or name", named, change)
			}
		}
	}
}

func mintClientCA(t *testing.T) string {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "conformance client CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func mintOriginCertificate(t *testing.T, hostname string) edge.OriginCertificate {
	t.Helper()
	certificate, key := mintCertificate(t, hostname, x509.ExtKeyUsageServerAuth)
	return edge.OriginCertificate{ID: "conformance-origin-certificate", Certificate: certificate, Key: key, ExpiresAt: time.Now().Add(365 * 24 * time.Hour)}
}

func mintCertificate(t *testing.T, hostname string, usage x509.ExtKeyUsage) (certificate, key string) {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

const conformancePreviewKey edge.PreviewKey = "conformance-preview-key"

func listPreviewHosts(fixture Fixture, base, token string) []edge.PreviewHost {
	if base == "" {
		base = "preview.conformance.invalid"
	}
	return edge.NewSharedPreviewSite(fixture.Spec.Slug, base, conformancePreviewKey).ListHosts("conformance-preview", token, []string{App})
}

func runPreviews(t *testing.T, suite Suite, record func(app, release string) router.ReleaseRecord) {
	t.Run("a preview pointer leaves nothing served when it is removed", func(t *testing.T) {
		if suite.Previews == nil {
			t.Skip("this router cannot be served on a preview wildcard from the conformance suite alone")
		}
		fixture := suite.Previews(t)
		stack := reconciled(t, fixture)
		const pointer = "conformance-preview"
		hosts := listPreviewHosts(fixture, suite.PreviewBase, "aliasaliasaliasa")
		movePreview(t, stack, pointer, "previewed", hosts, record(App, "b1"))
		if served := fixture.Serving(pointer); served != "b1" {
			t.Fatalf("%s serves %q, want the b1 this preview moved onto; a preview that never landed makes every assertion after it vacuous", pointer, served)
		}

		removePreview(t, stack, router.PointerRemoval{Pointer: pointer, Hosts: hosts})
		if served := fixture.Serving(pointer); served != "" {
			t.Errorf("%s serves %q once the preview is gone, want nothing", pointer, served)
		}
		removePreview(t, stack, router.PointerRemoval{Pointer: pointer, Hosts: hosts})
	})

	t.Run("a preview deployment keeps serving its release after the preview moves on, until it is removed", func(t *testing.T) {
		if suite.Previews == nil {
			t.Skip("this router cannot be served on a preview wildcard from the conformance suite alone")
		}
		fixture := suite.Previews(t)
		if !fixture.Router.Facts().ServesPreviewDeployments {
			t.Skip("Facts().ServesPreviewDeployments is false, so a preview is served on its alias alone")
		}
		stack := reconciled(t, fixture)
		const pointer = "conformance-preview"
		alias := listPreviewHosts(fixture, suite.PreviewBase, "aliasaliasaliasa")
		first := router.FormatDeploymentPointer(pointer, "previewed-1")
		firstHosts := listPreviewHosts(fixture, suite.PreviewBase, "firstfirstfirstf")
		movePreview(t, stack, pointer, "previewed-1", alias, record(App, "b1"))
		movePreview(t, stack, first, "previewed-1", firstHosts, record(App, "b1"))
		movePreview(t, stack, pointer, "previewed-2", alias, record(App, "b2"))
		movePreview(t, stack, router.FormatDeploymentPointer(pointer, "previewed-2"), "previewed-2", listPreviewHosts(fixture, suite.PreviewBase, "secondsecondseco"), record(App, "b2"))

		if served := fixture.Serving(pointer); served != "b2" {
			t.Errorf("%s serves %q, want the b2 the preview moved onto last", pointer, served)
		}
		if served := fixture.Serving(first); served != "b1" {
			t.Errorf("%s serves %q after the preview moved on, want the b1 it was deployed with", first, served)
		}
		removePreview(t, stack, router.PointerRemoval{Pointer: first, Hosts: firstHosts})
		if served := fixture.Serving(first); served != "" {
			t.Errorf("%s serves %q once it was removed, want nothing", first, served)
		}
		if served := fixture.Serving(pointer); served != "b2" {
			t.Errorf("%s serves %q after an older deployment was removed, want the b2 it served", pointer, served)
		}
	})
}

func movePreview(t *testing.T, stack router.Stack, pointer, promotionID string, hosts []edge.PreviewHost, records ...router.ReleaseRecord) {
	t.Helper()
	move := newPointerMove(promotionID, records...)
	move.Pointer, move.Hosts = pointer, hosts
	if err := stack.MovePointer(context.Background(), move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(%s onto %q): %v", promotionID, pointer, err)
	}
}

func removePreview(t *testing.T, stack router.Stack, removal router.PointerRemoval) {
	t.Helper()
	if err := stack.RemovePointer(context.Background(), removal, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(%q): %v", removal.Pointer, err)
	}
}

func racesAfterCheck(t *testing.T, fixture Fixture, pointer string, record func(app, release string) router.ReleaseRecord, check int) bool {
	t.Helper()
	ctx := context.Background()
	stack := reconciled(t, fixture)
	movePointer(t, stack, pointer, "conformance-b1", record(App, "b1"))
	racing, err := fixture.Router.Open(stack.State())
	if err != nil {
		t.Fatalf("Open a second stack onto the same state: %v", err)
	}

	ledger := &memoryLedger{active: "conformance-b1"}
	later := newPointerMove("conformance-b3", record(App, "b3"))
	later.Pointer = pointer
	earlier := newPointerMove("conformance-b2", record(App, "b2"))
	earlier.Pointer = pointer
	var raced error
	checks := 0
	earlier.StillActive = func(ctx context.Context) error {
		checks++
		if err := ledger.stillActive(earlier.Promotion.PromotionID)(ctx); err != nil {
			return err
		}
		if checks == check {
			raced = ledger.promote(ctx, racing, later, nil)
		}
		return nil
	}

	moved := ledger.promote(ctx, stack, earlier, earlier.StillActive)
	if checks < check {
		if check == 1 {
			t.Fatalf("MovePointer never asked StillActive (it returned %v), so no promote could race it", moved)
		}
		t.Skipf("MovePointer asked StillActive %d times, so no promote races it after call %d", checks, check)
	}
	releases := map[string]string{"conformance-b1": "b1", "conformance-b2": "b2", "conformance-b3": "b3"}
	if served, named := fixture.Serving(pointer), releases[ledger.active]; served != named {
		t.Errorf("%s serves %q while the ledger names %s (%s): a promote whose StillActive answered before another promoted and moved must not land over it (the pointer move it raced returned %v, its own pointer move %v)",
			pointer, served, ledger.active, named, raced, moved)
	}
	return true
}

func functionRecord(app, release string) router.ReleaseRecord {
	entry := "conformance-prod-" + app + "-r0a1b2c3d"
	return router.ReleaseRecord{
		App:           app,
		Release:       release,
		Entry:         "/",
		EntryFunction: entry,
		FunctionURLs:  map[string]string{"/": "https://conformance-" + app + ".example.com/"},
		Revisions:     map[string]string{entry: entry + "-" + release},
	}
}

func reconciled(t *testing.T, fixture Fixture) router.Stack {
	t.Helper()
	stack, err := fixture.Router.Reconcile(context.Background(), fixture.Spec, fixture.Prior)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return stack
}

func newPointerMove(promotionID string, records ...router.ReleaseRecord) router.PointerMove {
	move := router.PointerMove{
		Promotion: router.Promotion{PromotionID: promotionID, Ts: 1, Releases: map[string]string{}},
		Records:   map[string]router.ReleaseRecord{},
	}
	for _, record := range records {
		move.Promotion.Releases[record.App] = record.Release
		move.Records[record.App] = record
	}
	return move
}

func movePointer(t *testing.T, stack router.Stack, pointer, promotionID string, records ...router.ReleaseRecord) {
	t.Helper()
	move := newPointerMove(promotionID, records...)
	move.Pointer = pointer
	if err := stack.MovePointer(context.Background(), move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(%s onto %q): %v", promotionID, pointer, err)
	}
}

func removesPointer(t *testing.T, stack router.Stack, pointer string) {
	t.Helper()
	if err := stack.RemovePointer(context.Background(), router.PointerRemoval{Pointer: pointer}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(%q): %v", pointer, err)
	}
}

func roundTrip(t *testing.T, state router.StackState) router.StackState {
	t.Helper()
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal %+v: %v", state, err)
	}
	var read router.StackState
	if err := json.Unmarshal(payload, &read); err != nil {
		t.Fatalf("unmarshal %s: %v", payload, err)
	}
	return read
}

type memoryLedger struct {
	active string
}

func (l *memoryLedger) stillActive(promotionID string) router.StillActive {
	return func(context.Context) error {
		if l.active != promotionID {
			return errDisplaced
		}
		return nil
	}
}

func (l *memoryLedger) promote(ctx context.Context, stack router.Stack, move router.PointerMove, stillActive router.StillActive) error {
	displaced := l.active
	l.active = move.Promotion.PromotionID
	if stillActive == nil {
		stillActive = l.stillActive(move.Promotion.PromotionID)
	}
	move.StillActive = stillActive
	err := stack.MovePointer(ctx, move, progress.Discard())
	var unserved router.Unserved
	if errors.As(err, &unserved) && l.active == move.Promotion.PromotionID {
		l.active = displaced
	}
	return err
}
