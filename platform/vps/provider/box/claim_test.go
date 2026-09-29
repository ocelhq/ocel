package box_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const pulled = "-----BEGIN CERTIFICATE-----\nthe zone's client certificate\n-----END CERTIFICATE-----\n"

func routedOn(t *testing.T) (*machine, router.Stack) {
	t.Helper()
	m, front, stack := reconciled(t)
	routed, err := box.NewRouter(front.Edge).Open(router.NewStackState(stack.State()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m, routed
}

func TestAClaimOnTheBoxShieldsTheHostnameBeforeItTakesItAndNamesTheBoxAsItsOrigin(t *testing.T) {
	m, routed := routedOn(t)

	origin, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", ClientCertificates: []string{pulled}})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if origin.Address != address {
		t.Errorf("Claim named origin %+v, want the box at %s: the edge in front forwards the hostname there", origin, address)
	}
	surface := box.Surface(slug, environment.TierProduction)
	want := host.Shield{Hostname: "shop.example.com", Owner: surface, ClientCertificates: []string{pulled}}
	if len(m.shields) != 1 || !reflect.DeepEqual(m.shields[0], want) {
		t.Errorf("the box shields %+v, want %+v: the proxy answers the hostname only to a client presenting that certificate", m.shields, want)
	}
	if !slices.Contains(m.claims, host.HostClaim{Owner: surface, Hostname: "shop.example.com", Pointer: router.DefaultPointer, App: "web"}) {
		t.Errorf("the box claims %+v, want shop.example.com among them", m.claims)
	}
	if shielded, claimed := slices.Index(m.calls, "shield shop.example.com"), slices.Index(m.calls, "claim shop.example.com"); shielded < 0 || shielded > claimed {
		t.Errorf("calls = %v, want the hostname shielded before it is claimed: a claimed hostname nothing shields is answered to anyone", m.calls)
	}
	if !slices.Contains(m.calls, "apply origins "+slug+"/"+string(environment.TierProduction)) {
		t.Errorf("calls = %v, want the project's buckets brought in line with the hostname it now claims", m.calls)
	}
}

func TestDisclaimingOnTheBoxGivesTheHostnameAndItsShieldBack(t *testing.T) {
	m, routed := routedOn(t)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", ClientCertificates: []string{pulled}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := routed.Disclaim(context.Background(), "shop.example.com"); err != nil {
		t.Fatalf("Disclaim: %v", err)
	}
	if slices.ContainsFunc(m.claims, func(claim host.HostClaim) bool { return claim.Hostname == "shop.example.com" }) {
		t.Errorf("the box still claims %+v, want shop.example.com given back", m.claims)
	}
	if len(m.shields) != 0 {
		t.Errorf("the box still shields %+v, want nothing left of shop.example.com", m.shields)
	}
}

func TestDestroyingTheBoxRouterLeavesNothingClaimedRoutedOrNetworked(t *testing.T) {
	m, routed := routedOn(t)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := routed.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(m.claims) != 0 {
		t.Errorf("the box still claims %+v after the router was destroyed", m.claims)
	}
	surface := box.Surface(slug, environment.TierProduction)
	for _, call := range []string{"unroute " + surface, "forget network " + string(environment.TierProduction) + "/" + slug} {
		if !slices.Contains(m.calls, call) {
			t.Errorf("calls = %v, want %q: an edge in front forwards to the box and takes none of it down", m.calls, call)
		}
	}
}

func TestTheBoxRoutersRemovalPlanNamesTheRouteOfEveryHostnameAnEdgeForwardsToIt(t *testing.T) {
	_, front, _ := reconciled(t)

	var routes []string
	for _, group := range box.NewRouter(front.Edge).ProjectRemovals(edge.ProjectScope{
		Slug: slug, Tier: environment.TierProduction, Hostnames: []string{"shop.example.com"}, Front: address,
	}) {
		for _, change := range group.Changes {
			if change.Kind == box.RouteKind && change.Action == edge.PlanDelete {
				routes = append(routes, change.Name)
			}
		}
	}
	if !slices.Equal(routes, []string{"shop.example.com"}) {
		t.Errorf("the box router plans to delete routes %v, want shop.example.com: an edge in front lists only what it forwards, and the plan must name what the box takes down", routes)
	}
}

func originCertificate(t *testing.T, hostname string, life time.Duration) edge.OriginCertificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(life),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return edge.OriginCertificate{
		ID:          "origin-1",
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Key:         string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		ExpiresAt:   template.NotAfter,
	}
}

func TestTheBoxSaysItHoldsNoCertificateForAShieldedHostnameUntilTheEdgeIssuesOne(t *testing.T) {
	m, routed := routedOn(t)
	ctx := context.Background()
	claim := router.Claim{Hostname: "shop.example.com", App: "web", ClientCertificates: []string{pulled}}

	origin, err := routed.Claim(ctx, claim)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if origin.Certified {
		t.Fatal("the box says it holds a certificate for shop.example.com, and it holds none: one ordered on demand through the edge stalls wherever the zone redirects plain HTTP")
	}

	claim.OriginCertificate = originCertificate(t, "shop.example.com", 365*24*time.Hour)
	if origin, err = routed.Claim(ctx, claim); err != nil || !origin.Certified {
		t.Fatalf("Claim with an origin certificate = %+v, %v, want the box certified", origin, err)
	}
	if len(m.shields) != 1 || m.shields[0].Certificate != claim.OriginCertificate.Certificate || m.shields[0].Key != claim.OriginCertificate.Key {
		t.Errorf("the box shields %+v, want the origin certificate and its key held for shop.example.com", m.shields)
	}

	claim.OriginCertificate = edge.OriginCertificate{}
	if origin, err = routed.Claim(ctx, claim); err != nil || !origin.Certified {
		t.Errorf("Claim again = %+v, %v, want the box still certified by the certificate it holds", origin, err)
	}
}

func TestTheBoxSaysACertificateDueForRenewalCertifiesNothing(t *testing.T) {
	_, routed := routedOn(t)
	claim := router.Claim{
		Hostname: "shop.example.com", App: "web", ClientCertificates: []string{pulled},
		OriginCertificate: originCertificate(t, "shop.example.com", 24*time.Hour),
	}

	origin, err := routed.Claim(context.Background(), claim)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if origin.Certified {
		t.Error("the box says a certificate expiring tomorrow certifies shop.example.com, so no renewal is ever issued")
	}
}
