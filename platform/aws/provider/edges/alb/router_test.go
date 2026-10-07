package alb

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
)

const (
	testHostname    = "admin.shop.example"
	testCertificate = "arn:aws:acm:us-east-1:111122223333:certificate/admin"
	previewPointer  = "conformance-preview"
)

func containerRecord(f *fakeAWS) func(app, build string) router.ReleaseRecord {
	return func(app, build string) router.ReleaseRecord {
		physical := "ocel-shop-" + app + "-" + build
		f.release(physical, build)
		return router.ReleaseRecord{App: app, Release: build, Physical: physical}
	}
}

func fixtureOn(t *testing.T, f *fakeAWS, tier environment.Tier, pointer string) routerconformance.Fixture {
	t.Helper()
	r := newTestRouter(f)
	spec := router.StackSpec{Tier: tier, Slug: "shop"}
	stack, err := r.Reconcile(context.Background(), spec, router.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	host := pointer + "." + testHostname
	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: host, App: routerconformance.App, Pointer: pointer, Certificate: testCertificate}); err != nil {
		t.Fatalf("Claim(%s): %v", host, err)
	}
	return routerconformance.Fixture{
		Router: r,
		Spec:   spec,
		Prior:  stack.State(),
		Serving: func(served string) string {
			if router.ResolvePointer(served) != router.ResolvePointer(pointer) {
				return ""
			}
			return f.servedBy(host)
		},
		FailNextPointerMove: func(err error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.failModify = err
		},
	}
}

func TestTheALBRouterBehavesAsEveryRouterMust(t *testing.T) {
	var current *fakeAWS
	routerconformance.Run(t, routerconformance.Suite{
		New: func(t *testing.T) routerconformance.Fixture {
			current = newFakeAWS()
			return fixtureOn(t, current, environment.TierProduction, router.DefaultPointer)
		},
		Previews: func(t *testing.T) routerconformance.Fixture {
			current = newFakeAWS()
			return fixtureOn(t, current, environment.TierPreview, previewPointer)
		},
		Hostname: testHostname,
		Record: func(app, build string) router.ReleaseRecord {
			return containerRecord(current)(app, build)
		},
	})
}

func newTestRouter(f *fakeAWS) Router {
	r := NewRouter(f.clients)
	r.pause = func(context.Context, time.Duration) error { return nil }
	return r
}

func claimedStack(t *testing.T, f *fakeAWS, claims ...router.Claim) router.Stack {
	t.Helper()
	r := newTestRouter(f)
	stack, err := r.Reconcile(context.Background(), router.StackSpec{Tier: environment.TierProduction, Slug: "shop"}, router.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, claim := range claims {
		if _, err := stack.Claim(context.Background(), claim); err != nil {
			t.Fatalf("Claim(%s): %v", claim.Hostname, err)
		}
	}
	return stack
}

func moveOnto(t *testing.T, stack router.Stack, pointer string, records ...router.ReleaseRecord) error {
	t.Helper()
	move := router.PointerMove{Pointer: pointer, Promotion: router.Promotion{PromotionID: "p", Releases: map[string]string{}}, Records: map[string]router.ReleaseRecord{}}
	for _, record := range records {
		move.Promotion.Releases[record.App] = record.Release
		move.Records[record.App] = record
	}
	return stack.MovePointer(context.Background(), move, progress.Discard())
}

func TestAClaimAnswersOnThePublicBalancerWithTheHostnamesCertificateAndARuleForIt(t *testing.T) {
	f := newFakeAWS()
	r := newTestRouter(f)
	stack, err := r.Reconcile(context.Background(), router.StackSpec{Tier: environment.TierProduction, Slug: "shop"}, router.StackState{})
	if err != nil {
		t.Fatal(err)
	}

	origin, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if origin.Address != testAddress || !origin.Certified {
		t.Errorf("Claim named origin %+v, want %s answering with a certificate of its own", origin, testAddress)
	}
	if held := f.heldCertificates(); !slices.Equal(held, []string{testCertificate}) {
		t.Errorf("the listener holds certificates %v, want %s: it answers %s by SNI", held, testCertificate, testHostname)
	}
	if hosts := f.ruleHosts(); !slices.Equal(hosts, []string{testHostname}) {
		t.Errorf("the listener routes hosts %v, want one rule for %s", hosts, testHostname)
	}
	if served := f.servedBy(testHostname); served != "" {
		t.Errorf("%s forwards to %q before any promote, want nothing", testHostname, served)
	}
}

func TestAClaimOnAnActivePointerForwardsStraightToTheReleaseItServes(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f)
	if err := moveOnto(t, stack, "", containerRecord(f)("admin", "b1")); err != nil {
		t.Fatalf("MovePointer: %v", err)
	}

	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if served := f.servedBy(testHostname); served != "b1" {
		t.Errorf("%s claimed after a promote forwards to %q, want b1, the release its pointer serves", testHostname, served)
	}
}

func TestAPromoteFlipsEveryHostnameOfTheAppOnItsPointerAndNoOther(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f,
		router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate},
		router.Claim{Hostname: "ops.shop.example", App: "admin", Certificate: testCertificate},
		router.Claim{Hostname: "api.shop.example", App: "api", Certificate: testCertificate},
		router.Claim{Hostname: "admin-pr-7.preview.example", App: "admin", Pointer: "pr-7", Certificate: testCertificate},
	)

	if err := moveOnto(t, stack, router.DefaultPointer, containerRecord(f)("admin", "b1")); err != nil {
		t.Fatalf("MovePointer: %v", err)
	}
	for _, host := range []string{testHostname, "ops.shop.example"} {
		if served := f.servedBy(host); served != "b1" {
			t.Errorf("%s forwards to %q, want b1", host, served)
		}
	}
	for _, host := range []string{"api.shop.example", "admin-pr-7.preview.example"} {
		if served := f.servedBy(host); served != "" {
			t.Errorf("%s forwards to %q, want nothing: it serves another app or another pointer", host, served)
		}
	}
}

func TestAPromoteOfAReleaseTheClusterDoesNotRunIsUnservedAndFlipsNothing(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f, router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})

	err := moveOnto(t, stack, "", router.ReleaseRecord{App: "admin", Release: "b1", Physical: "ocel-shop-admin-gone"})
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer onto a service the cluster does not run = %v, want router.Unserved", err)
	}
	if f.calls["ModifyRule"] != 0 {
		t.Errorf("modified %d rules, want none", f.calls["ModifyRule"])
	}
}

func TestDisclaimingAHostnameDropsItsRuleAndTheCertificateNothingElseAnswersWith(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f,
		router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate, CertificateRequested: true},
		router.Claim{Hostname: "ops.shop.example", App: "admin", Certificate: "arn:aws:acm:us-east-1:111122223333:certificate/ops", CertificateRequested: true},
	)

	if err := stack.Disclaim(context.Background(), testHostname); err != nil {
		t.Fatalf("Disclaim: %v", err)
	}
	if hosts := f.ruleHosts(); !slices.Equal(hosts, []string{"ops.shop.example"}) {
		t.Errorf("the listener routes %v, want ops.shop.example alone", hosts)
	}
	if held := f.heldCertificates(); !slices.Equal(held, []string{"arn:aws:acm:us-east-1:111122223333:certificate/ops"}) {
		t.Errorf("the listener holds %v, want the certificate of ops.shop.example alone", held)
	}
}

func TestRemovingAPreviewPointerDropsTheRulesOfItsHostsAlone(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f,
		router.Claim{Hostname: "admin-pr-7.preview.example", App: "admin", Pointer: "pr-7", Certificate: "arn:aws:acm:us-east-1:111122223333:certificate/wildcard"},
		router.Claim{Hostname: "admin-pr-8.preview.example", App: "admin", Pointer: "pr-8", Certificate: "arn:aws:acm:us-east-1:111122223333:certificate/wildcard"},
	)

	if err := stack.RemovePointer(context.Background(), router.PointerRemoval{Pointer: "pr-7"}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if hosts := f.ruleHosts(); !slices.Equal(hosts, []string{"admin-pr-8.preview.example"}) {
		t.Errorf("the listener routes %v, want pr-8's host alone", hosts)
	}
	if held := f.heldCertificates(); len(held) != 1 {
		t.Errorf("the listener holds %v, want the wildcard certificate pr-8 still answers with", held)
	}
}

func TestAClaimBeforeAnyContainerAppDeployedBehindCloudflareIsNotReady(t *testing.T) {
	f := newFakeAWS()
	f.absent = true
	stack := claimedStack(t, f)

	_, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Errorf("Claim with no public balancer = %v, want a refusal with code %s", err, refusal.CodeNotReady)
	}
}

func TestTheALBRouterServesStreamingAndNoNeedThatRunsCodeAtTheEdge(t *testing.T) {
	facts := newTestRouter(newFakeAWS()).Facts()
	if !slices.Equal(facts.Supported, []edge.Need{edge.NeedStreaming}) {
		t.Errorf("Facts().Supported = %v, want streaming alone: Cloudflare forwards to the balancer and runs none of the edge's code for these apps", facts.Supported)
	}
	if !facts.ReachesContainers || facts.ReachesFunctions {
		t.Errorf("Facts() = %+v, want containers reached and no function", facts)
	}
}

func TestAPointerMoveWhileAnotherPromoteHoldsThePointerMovesNothingAndIsUnserved(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f, router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})
	if err := moveOnto(t, stack, router.DefaultPointer, containerRecord(f)("admin", "b1")); err != nil {
		t.Fatalf("MovePointer: %v", err)
	}

	f.hold("ocel/routes/production/shop/@production.lease")
	err := moveOnto(t, stack, router.DefaultPointer, containerRecord(f)("admin", "b2"))
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Errorf("MovePointer while another promote holds the pointer = %v, want router.Unserved", err)
	}
	if served := f.servedBy(testHostname); served != "b1" {
		t.Errorf("%s forwards to %q, want b1: the load balancer cannot compare-and-set a rule, so one promote at a time moves a pointer, and the one holding it restores what it read if the ledger displaced it", testHostname, served)
	}
}
