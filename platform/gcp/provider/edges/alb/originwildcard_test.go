package alb

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	originBase        = "o.example.com"
	originCertificate = "projects/acme-prod/locations/global/certificates/origin"
)

func mintCertificateEndingAt(name string, notAfter time.Time) string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: notAfter.Add(-48 * time.Hour), NotAfter: notAfter,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:        true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func originWildcardSpec(ca string) OriginWildcardSpec {
	return OriginWildcardSpec{Tier: environment.TierProduction, BaseDomain: originBase, Certificate: originCertificate, ClientCAs: []string{ca}}
}

func shieldedRaises(w *world) int {
	count := 0
	for _, up := range w.raised() {
		if up == ShieldedLoadBalancerStack(environment.TierProduction) {
			count++
		}
	}
	return count
}

func TestTheShieldedLoadBalancerAnswersTheOriginWildcardOnItsOwnCertificate(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)

	address, err := balancer.Shielded().ReconcileOriginWildcard(context.Background(), originWildcardSpec(zonePull))
	if err != nil {
		t.Fatalf("ReconcileOriginWildcard = %v", err)
	}

	if address != shieldedAddress {
		t.Errorf("address = %q, want the shielded balancer's %q", address, shieldedAddress)
	}
	entry, declared := w.declarations(ShieldedLoadBalancerStack(environment.TierProduction))[originEntryName(environment.TierProduction, originBase)]
	if !declared {
		t.Fatal("the shielded program enters no certificate for the origin wildcard")
	}
	if entry.Args["hostname"] != "*."+originBase {
		t.Errorf("entry hostname = %v, want *.%s", entry.Args["hostname"], originBase)
	}
	if certificates := asList(entry.Args["certificates"]); len(certificates) != 1 || certificates[0] != originCertificate {
		t.Errorf("entry certificates = %v, want %s", certificates, originCertificate)
	}
}

func TestTheShieldedLoadBalancerTrustsTheWorkersClientCertificateForTheOriginWildcard(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)

	if _, err := balancer.Shielded().ReconcileOriginWildcard(context.Background(), originWildcardSpec(zonePull)); err != nil {
		t.Fatal(err)
	}
	if got := trustedBy(t, w); !slices.Equal(got, []string{zonePull}) {
		t.Errorf("trusted = %d certificates, want the worker's CA alone", len(got))
	}
}

func TestReconcilingTheOriginWildcardTwiceChangesNothing(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	first, err := shielded.ReconcileOriginWildcard(context.Background(), originWildcardSpec(zonePull))
	if err != nil {
		t.Fatal(err)
	}
	raised := shieldedRaises(w)

	second, err := shielded.ReconcileOriginWildcard(context.Background(), originWildcardSpec(zonePull))
	if err != nil {
		t.Fatal(err)
	}
	if first != second || shieldedRaises(w) != raised {
		t.Errorf("the second reconcile answered %q after %d raises, want %q and %d", second, shieldedRaises(w), first, raised)
	}
}

func TestAnotherOriginBaseOnTheSameTierIsRefused(t *testing.T) {
	t.Parallel()
	balancer, _ := shielding(t)
	shielded := balancer.Shielded()
	if _, err := shielded.ReconcileOriginWildcard(context.Background(), originWildcardSpec(zonePull)); err != nil {
		t.Fatal(err)
	}

	other := originWildcardSpec(zonePull)
	other.BaseDomain = "x.example.com"
	_, err := shielded.ReconcileOriginWildcard(context.Background(), other)
	if code := refusalCodeOf(err); code != refusal.CodeInvalid || !strings.Contains(err.Error(), originBase) {
		t.Errorf("ReconcileOriginWildcard(another base) = %v, want an invalid refusal naming %s", err, originBase)
	}
}

func refusalCodeOf(err error) refusal.Code {
	var refused refusal.Refusal
	if errors.As(err, &refused) {
		return refused.Code
	}
	return ""
}

func TestARenewedWorkerCertificateIsTrustedBesideTheOneItReplacesUntilThatExpires(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	old := mintCertificateEndingAt("old", time.Now().Add(365*24*time.Hour))
	renewed := mintCertificateEndingAt("renewed", time.Now().Add(2*365*24*time.Hour))
	ctx := context.Background()
	if _, err := shielded.ReconcileOriginWildcard(ctx, originWildcardSpec(old)); err != nil {
		t.Fatal(err)
	}

	if _, err := shielded.ReconcileOriginWildcard(ctx, originWildcardSpec(renewed)); err != nil {
		t.Fatal(err)
	}
	if got, want := trustedBy(t, w), slices.Sorted(slices.Values([]string{old, renewed})); !slices.Equal(got, want) {
		t.Errorf("trusted %d certificates, want the renewed CA beside the old one: workers that have not redeployed still present the old", len(got))
	}

	expired := mintCertificateEndingAt("expired", time.Now().Add(-24*time.Hour))
	if _, err := shielded.changeTrust(ctx, environment.TierProduction, func(read trustRecord) trustRecord {
		return read.recordClaim("*."+originBase, []string{expired, renewed})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := shielded.ReconcileOriginWildcard(ctx, originWildcardSpec(renewed)); err != nil {
		t.Fatal(err)
	}
	if got := trustedBy(t, w); !slices.Equal(got, []string{renewed}) {
		t.Errorf("trusted %d certificates, want the expired CA dropped and the CA the call names kept", len(got))
	}
}

func TestEveryRaiseOfTheShieldedLoadBalancerKeepsTheOriginWildcard(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	if _, err := shielded.ReconcileOriginWildcard(ctx, originWildcardSpec(zonePull)); err != nil {
		t.Fatal(err)
	}

	if _, err := shielded.trustClaim(ctx, environment.TierProduction, "shop.example.org", []string{otherZonePull}); err != nil {
		t.Fatal(err)
	}
	if _, kept := w.declarations(ShieldedLoadBalancerStack(environment.TierProduction))[originEntryName(environment.TierProduction, originBase)]; !kept {
		t.Error("a raise for another hostname's trust dropped the origin wildcard's certificate map entry")
	}
}

func TestOnlyTheShieldedLoadBalancerServesAnOriginWildcard(t *testing.T) {
	t.Parallel()
	balancer, _ := shielding(t)

	_, err := balancer.ReconcileOriginWildcard(context.Background(), originWildcardSpec(zonePull))
	if refusalCodeOf(err) != refusal.CodeInvalid {
		t.Errorf("ReconcileOriginWildcard on the unshielded edge = %v, want an invalid refusal", err)
	}
	for name, spec := range map[string]OriginWildcardSpec{
		"no base":        {Tier: environment.TierProduction, Certificate: originCertificate, ClientCAs: []string{zonePull}},
		"no certificate": {Tier: environment.TierProduction, BaseDomain: originBase, ClientCAs: []string{zonePull}},
		"no client CA":   {Tier: environment.TierProduction, BaseDomain: originBase, Certificate: originCertificate},
	} {
		if _, err := balancer.Shielded().ReconcileOriginWildcard(context.Background(), spec); refusalCodeOf(err) != refusal.CodeInvalid {
			t.Errorf("%s: ReconcileOriginWildcard = %v, want an invalid refusal", name, err)
		}
	}
}

func TestDestroyingTheOriginWildcardTakesItsEntryAndTrustOff(t *testing.T) {
	t.Parallel()
	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	if _, err := shielded.ReconcileOriginWildcard(ctx, originWildcardSpec(zonePull)); err != nil {
		t.Fatal(err)
	}

	if err := shielded.DestroyOriginWildcard(ctx, environment.TierProduction); err != nil {
		t.Fatalf("DestroyOriginWildcard = %v", err)
	}
	if _, left := w.declarations(ShieldedLoadBalancerStack(environment.TierProduction))[originEntryName(environment.TierProduction, originBase)]; left {
		t.Error("the shielded program still enters the origin wildcard")
	}
	if _, record, err := shielded.readTrust(ctx, environment.TierProduction); err != nil {
		t.Fatal(err)
	} else if _, claimed := record.Hostnames["*."+originBase]; claimed {
		t.Error("the trust record still holds a claim for the origin wildcard")
	}
	if err := shielded.DestroyOriginWildcard(ctx, environment.TierProduction); err != nil {
		t.Errorf("destroying what is gone = %v, want nothing to do", err)
	}
}
