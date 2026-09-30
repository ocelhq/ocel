package alb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestAClaimTrustsTheClientCAsOfEveryZoneThatClaimedThroughIt(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f)
	first, second := mintCA(t, "zone one"), mintCA(t, "zone two")

	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate, ClientCAs: []string{first}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if f.listenerMode != "verify" || f.listenerTrust == "" {
		t.Errorf("the listener checks client certificates in mode %q against %q, want verify against the trust store", f.listenerMode, f.listenerTrust)
	}
	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: "ops.other.example", App: "admin", Certificate: testCertificate, ClientCAs: []string{second}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	trusted := f.trusted()
	if !strings.Contains(trusted, strings.TrimSpace(first)) || !strings.Contains(trusted, strings.TrimSpace(second)) {
		t.Errorf("the trust store holds\n%s\nwant both zones' CAs: one tier's balancer answers every zone's hostnames, and a bundle of the last zone alone refuses the others", trusted)
	}
	if strings.Contains(trusted, "\n\n") {
		t.Errorf("the trust store bundle has a blank line, which the load balancer refuses:\n%s", trusted)
	}
}

func TestAClaimRetriedAfterTheTrustStoreRefusedAChangeAppliesIt(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f)
	first, second := mintCA(t, "zone one"), mintCA(t, "zone two")
	if _, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate, ClientCAs: []string{first}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	f.failTrustModify = errors.New("throttled")
	claim := router.Claim{Hostname: "ops.other.example", App: "admin", Certificate: testCertificate, ClientCAs: []string{second}}
	if _, err := stack.Claim(context.Background(), claim); err == nil {
		t.Fatal("Claim while the trust store refused the change = nil, want the failure")
	}
	if _, err := stack.Claim(context.Background(), claim); err != nil {
		t.Fatalf("Claim again: %v", err)
	}
	if trusted := f.trusted(); !strings.Contains(trusted, strings.TrimSpace(second)) {
		t.Errorf("the trust store holds\n%s\nwant zone two's CA: the retry must compare against what the trust store holds, not against what the failed run already wrote to the bucket", trusted)
	}
}

func TestDisclaimingTheLastHostnameThatTrustsACAPrunesItFromTheTrustStore(t *testing.T) {
	f := newFakeAWS()
	first, second := mintCA(t, "zone one"), mintCA(t, "zone two")
	stack := claimedStack(t, f,
		router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate, ClientCAs: []string{first}},
		router.Claim{Hostname: "ops.other.example", App: "admin", Certificate: testCertificate, ClientCAs: []string{second}},
	)

	if err := stack.Disclaim(context.Background(), "ops.other.example"); err != nil {
		t.Fatalf("Disclaim: %v", err)
	}
	trusted := f.trusted()
	if strings.Contains(trusted, strings.TrimSpace(second)) || !strings.Contains(trusted, strings.TrimSpace(first)) {
		t.Errorf("the trust store holds\n%s\nwant zone one's CA alone: no hostname left trusts zone two's, and a trust store holds at most 25 CAs", trusted)
	}
}

func TestAClaimTrustingACertificateThatIsNoCAIsRefusedBeforeTheTrustStoreIsTouched(t *testing.T) {
	f := newFakeAWS()
	stack := claimedStack(t, f)
	authority := mintCA(t, "zone one")
	notCA := mintLeaf(t)

	_, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate, ClientCAs: []string{authority, notCA}})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(refused.Message, "CA") {
		t.Errorf("Claim trusting a certificate that is no CA = %v, want an invalid refusal: the load balancer's trust store refuses a bundle holding one", err)
	}
	if f.trustStore != "" {
		t.Errorf("the trust store was raised as %q, want nothing raised for a claim that was refused", f.trustStore)
	}
}

func TestATrustStorePastItsCAQuotaIsRefusedNamingTheServiceQuota(t *testing.T) {
	f := newFakeAWS()
	f.failTrustCreate = &elbv2types.InvalidCaCertificatesBundleException{Message: aws.String("The CA certificates bundle has too many certificates")}
	stack := claimedStack(t, f)

	_, err := stack.Claim(context.Background(), router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate, ClientCAs: []string{mintCA(t, "zone one")}})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "L-43FE5A42") {
		t.Errorf("Claim past the trust store's CA quota = %v, want a refusal naming the Service Quotas code to raise", err)
	}
}

func TestADisclaimRetriedAfterTheTrustRecordRefusedTheChangePrunesTheCA(t *testing.T) {
	f := newFakeAWS()
	first, second := mintCA(t, "zone one"), mintCA(t, "zone two")
	stack := claimedStack(t, f,
		router.Claim{Hostname: testHostname, App: "admin", Certificate: testCertificate, ClientCAs: []string{first}},
		router.Claim{Hostname: "ops.other.example", App: "admin", Certificate: testCertificate, ClientCAs: []string{second}},
	)

	f.failPut = errors.New("throttled")
	if err := stack.Disclaim(context.Background(), "ops.other.example"); err == nil {
		t.Fatal("Disclaim while the trust record refused the change = nil, want the failure")
	}
	if err := stack.Disclaim(context.Background(), "ops.other.example"); err != nil {
		t.Fatalf("Disclaim again: %v", err)
	}
	if trusted := f.trusted(); strings.Contains(trusted, strings.TrimSpace(second)) {
		t.Errorf("the trust store holds\n%s\nwant zone two's CA pruned: the hostname stays recorded until what it held is released, so a retry finishes the job", trusted)
	}
}
