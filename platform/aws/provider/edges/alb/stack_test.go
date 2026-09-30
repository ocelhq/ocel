package alb

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestAHostnameClaimedAgainForAnotherAppForwardsToThatAppsRelease(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f, router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})
	if err := moveOnto(t, stack, router.DefaultPointer, containerRecord(f)("admin", "b1"), containerRecord(f)("api", "b2")); err != nil {
		t.Fatalf("MovePointer: %v", err)
	}

	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "api", Certificate: testCertificate}); err != nil {
		t.Fatalf("Claim for api: %v", err)
	}
	if served := f.servedBy(testHostname); served != "b2" {
		t.Errorf("%s claimed for api forwards to %q, want b2, the release api is promoted to", testHostname, served)
	}
}

func TestAHostnameClaimedAgainOnAnotherPointerForwardsToWhatThatPointerServes(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f, router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})
	if err := moveOnto(t, stack, router.DefaultPointer, containerRecord(f)("admin", "b1")); err != nil {
		t.Fatalf("MovePointer: %v", err)
	}

	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "admin", Pointer: "pr-7", Certificate: testCertificate}); err != nil {
		t.Fatalf("Claim on pr-7: %v", err)
	}
	if served := f.servedBy(testHostname); served != "" {
		t.Errorf("%s claimed on pr-7, where nothing is promoted, forwards to %q, want nothing", testHostname, served)
	}
}

func TestDisclaimingAHostnameLeavesACertificateThisProjectDidNotRequestOnTheListener(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f, router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})

	if err := stack.Disclaim(context.Background(), testHostname); err != nil {
		t.Fatalf("Disclaim: %v", err)
	}
	if held := f.heldCertificates(); !slices.Equal(held, []string{testCertificate}) {
		t.Errorf("the listener holds %v, want %s kept: ocel did not request it for this project, so a hostname of another project in the tier may answer with it", held, testCertificate)
	}
}

func TestACertificatePastTheListenersQuotaIsRefusedNamingTheServiceQuota(t *testing.T) {
	f := newFakeAWS()
	f.certificateLimit = 1
	stack := claimedStack(t, f, router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})

	_, err := stack.Claim(context.Background(), router.Claim{Hostname: "ops.shop.example", App: "admin", Certificate: "arn:aws:acm:us-east-1:111122223333:certificate/ops"})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "L-9365A611") {
		t.Errorf("Claim past the listener's certificate quota = %v, want a refusal naming the Service Quotas code to raise", err)
	}
}

func TestARulePastTheListenersQuotaIsRefusedNamingTheServiceQuota(t *testing.T) {
	f := newFakeAWS()
	f.ruleLimit = 1
	stack := claimedStack(t, f, router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate})

	_, err := stack.Claim(context.Background(), router.Claim{Hostname: "ops.shop.example", App: "admin", Certificate: testCertificate})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "L-7EED9B64") {
		t.Errorf("Claim past the listener's rule quota = %v, want a refusal naming the Service Quotas code to raise", err)
	}
}
