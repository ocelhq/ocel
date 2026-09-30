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
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const shieldedAddress = "34.117.0.8"

var (
	zonePull      = mintTestCertificate("zone one", true)
	otherZonePull = mintTestCertificate("zone two", true)
	selfSigned    = mintTestCertificate("your own", false)
)

func mintTestCertificate(name string, authority bool) string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{"example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if authority {
		template.IsCA, template.BasicConstraintsValid, template.KeyUsage = true, true, x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func shieldedLoadBalancer() map[string]string {
	return map[string]string{
		"address":        shieldedAddress,
		"certificateMap": "ocel-alb-shielded-production-certs",
		"urlMap":         "ocel-alb-shielded-production-routes",
		"notFound":       "ocel-alb-shielded-production-notfound",
	}
}

func shielding(t *testing.T) (*Edge, *world) {
	t.Helper()
	balancer, w := balancing(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierProduction)] = shieldedLoadBalancer()
	return balancer, w
}

func unreconciledRouter(t *testing.T, balancer *Edge) router.Stack {
	t.Helper()
	routed, err := NewRouter(balancer).Open(router.NewStackState(edge.StackState{Slug: "shop", Tier: environment.TierProduction}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return routed
}

func trustedBy(t *testing.T, w *world) []string {
	t.Helper()
	trust, declared := w.declarations(ShieldedLoadBalancerStack(environment.TierProduction))["ocel-alb-shielded-production-trust"]
	if !declared {
		t.Fatal("the shielded balancer declares no trust config, so nothing checks the client certificate a request arrives with")
	}
	anchors, allowlisted := readTrustConfig(trust)
	return slices.Sorted(slices.Values(slices.Concat(anchors, allowlisted)))
}

func readTrustConfig(trust declaration) (anchors, allowlisted []string) {
	for _, store := range asList(trust.Args["trustStores"]) {
		for _, anchor := range asList(store.(map[string]any)["trustAnchors"]) {
			anchors = append(anchors, anchor.(map[string]any)["pemCertificate"].(string))
		}
	}
	for _, entry := range asList(trust.Args["allowlistedCertificates"]) {
		allowlisted = append(allowlisted, entry.(map[string]any)["pemCertificate"].(string))
	}
	return anchors, allowlisted
}

func asList(value any) []any {
	listed, _ := value.([]any)
	return listed
}

func TestTheShieldedLoadBalancerTrustsAClientCAAsATrustAnchorAndASelfSignedCertificateByAllowlistingIt(t *testing.T) {
	t.Parallel()

	seen, err := declared(loadBalancerProgram(loadBalancerSpec{Names: loadBalancerNames(environment.TierProduction, true), ClientCAs: []string{zonePull, selfSigned}}))
	if err != nil {
		t.Fatalf("the shielded balancer program = %v", err)
	}
	anchors, allowlisted := readTrustConfig(seen["ocel-alb-shielded-production-trust"])
	if !slices.Equal(anchors, []string{zonePull}) {
		t.Errorf("the trust config anchors %d certificates, want the client CA alone: a certificate chaining to it verifies only against a trust anchor", len(anchors))
	}
	if !slices.Equal(allowlisted, []string{selfSigned}) {
		t.Errorf("the trust config allowlists %d certificates, want the self-signed one alone: a load balancer refuses a self-signed client certificate unless it is allowlisted", len(allowlisted))
	}
}

func TestTheShieldedLoadBalancerRefusesEveryConnectionThatPresentsNoCertificateItTrusts(t *testing.T) {
	t.Parallel()

	seen, err := declared(loadBalancerProgram(loadBalancerSpec{Names: loadBalancerNames(environment.TierProduction, true), ClientCAs: []string{zonePull}}))
	if err != nil {
		t.Fatalf("the shielded balancer program = %v", err)
	}
	trust := seen["ocel-alb-shielded-production-trust"]
	if trust.Token != "gcp:certificatemanager/trustConfig:TrustConfig" || trust.Args["location"] != "global" {
		t.Fatalf("the trust config is %+v, want a global certificate manager trust config", trust)
	}
	policy := seen["ocel-alb-shielded-production-mtls"]
	if policy.Token != "gcp:networksecurity/serverTlsPolicy:ServerTlsPolicy" || policy.Args["location"] != "global" {
		t.Fatalf("the server tls policy is %+v, want a global network security server tls policy", policy)
	}
	mtls, _ := policy.Args["mtlsPolicy"].(map[string]any)
	if mtls["clientValidationMode"] != "REJECT_INVALID" {
		t.Errorf("the policy validates clients in mode %v, want REJECT_INVALID: a connection with no certificate or one it does not trust is refused", mtls["clientValidationMode"])
	}
	if got := mtls["clientValidationTrustConfig"]; got == nil || !strings.Contains(got.(string), "ocel-alb-shielded-production-trust") {
		t.Errorf("the policy validates clients against %v, want the shielded balancer's trust config", got)
	}
	proxy := seen["ocel-alb-shielded-production-https"]
	if got := proxy.Args["serverTlsPolicy"]; got == nil || !strings.Contains(got.(string), "ocel-alb-shielded-production-mtls") {
		t.Errorf("the https proxy carries server tls policy %v, want the shielded balancer's, or it answers any client", got)
	}

	plain, err := declared(loadBalancerProgram(loadBalancerSpec{Names: loadBalancerNames(environment.TierProduction, false)}))
	if err != nil {
		t.Fatalf("the balancer program = %v", err)
	}
	if got := plain["ocel-alb-production-https"].Args["serverTlsPolicy"]; got != nil {
		t.Errorf("the balancer no edge proxies carries server tls policy %v, want none: its clients are browsers", got)
	}
}

func TestAClaimWithAClientCertificateAnswersTheHostnameOnTheShieldedLoadBalancerAndNamesItAsTheOrigin(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	routed := unreconciledRouter(t, balancer)

	origin, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCAs: []string{zonePull}})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if origin != (edge.Origin{Address: shieldedAddress, Certified: true}) {
		t.Errorf("Claim named origin %+v, want the shielded balancer at %s, certified by the certificate the claim entered in its map", origin, shieldedAddress)
	}
	if got := trustedBy(t, w); !slices.Equal(got, []string{zonePull}) {
		t.Errorf("the shielded balancer trusts %v, want the client certificate the claim carried", got)
	}
	if _, routedThere := w.hosts("ocel-alb-shielded-production-routes")["shop.example.com"]; !routedThere {
		t.Errorf("the shielded balancer's url map routes %v, want shop.example.com", w.hosts("ocel-alb-shielded-production-routes"))
	}
	if _, routedPlain := w.hosts("ocel-alb-production-routes")["shop.example.com"]; routedPlain {
		t.Error("shop.example.com is routed on the balancer browsers reach directly, so a request that skips the edge would be answered")
	}
	entry := w.declarations(BindingStack("shop", environment.TierProduction))[entryName("shop", environment.TierProduction, "shop.example.com")]
	if entry.Args["map"] != "ocel-alb-shielded-production-certs" {
		t.Errorf("the hostname's certificate is entered in map %v, want the shielded balancer's", entry.Args["map"])
	}
}

func TestTheShieldedLoadBalancerIsRaisedAgainOnlyForACertificateItDoesNotYetTrust(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	routed := unreconciledRouter(t, balancer)
	ctx := context.Background()
	for _, hostname := range []string{"shop.example.com", "www.example.com"} {
		if _, err := routed.Claim(ctx, router.Claim{Hostname: hostname, App: "web", Certificate: "certs/" + hostname, ClientCAs: []string{zonePull}}); err != nil {
			t.Fatalf("Claim(%s): %v", hostname, err)
		}
	}
	raised := func() int {
		count := 0
		for _, up := range w.raised() {
			if up == ShieldedLoadBalancerStack(environment.TierProduction) {
				count++
			}
		}
		return count
	}
	if got := raised(); got != 1 {
		t.Errorf("the shielded balancer was raised %d times for two hostnames in one zone, want once", got)
	}

	if _, err := routed.Claim(ctx, router.Claim{Hostname: "shop.example.org", App: "web", Certificate: "certs/org", ClientCAs: []string{otherZonePull}}); err != nil {
		t.Fatalf("Claim in another zone: %v", err)
	}
	if got := raised(); got != 2 {
		t.Errorf("the shielded balancer was raised %d times, want again for another zone's certificate", got)
	}
	if got, want := trustedBy(t, w), slices.Sorted(slices.Values([]string{zonePull, otherZonePull})); !slices.Equal(got, want) {
		t.Errorf("the shielded balancer trusts %v, want both zones' certificates: every project forwarded through it keeps answering", got)
	}
}

func TestTheShieldedLoadBalancerTrustsExactlyWhatTheHostnamesClaimedOnItCarry(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierPreview)] = shieldedLoadBalancer()
	routed := unreconciledRouter(t, balancer)
	ctx := context.Background()
	claim := func(hostname string, certificates ...string) {
		t.Helper()
		if _, err := routed.Claim(ctx, router.Claim{Hostname: hostname, App: "web", Certificate: "certs/" + hostname, ClientCAs: certificates}); err != nil {
			t.Fatalf("Claim(%s): %v", hostname, err)
		}
	}
	uploaded := mintTestCertificate("zone one, uploaded beside it", true)
	claim("shop.example.com", zonePull)
	claim("www.example.com", zonePull)
	claim("shop.example.org", otherZonePull)
	if _, err := balancer.Shielded().trustClaim(ctx, environment.TierPreview, "*.preview.example.com", []string{zonePull}); err != nil {
		t.Fatal(err)
	}

	claim("shop.example.com", zonePull, uploaded)
	if got, want := trustedBy(t, w), slices.Sorted(slices.Values([]string{zonePull, uploaded, otherZonePull})); !slices.Equal(got, want) {
		t.Errorf("the shielded balancer trusts %v once you uploaded a second certificate to the zone, want %v", got, want)
	}
	_, preview, err := balancer.Shielded().readTrust(ctx, environment.TierPreview)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(preview.listTrusted(), uploaded) {
		t.Errorf("the preview tier's shielded balancer trusts %v, want the uploaded certificate too: the zone presents the certificate uploaded last to every origin it forwards to", preview.listTrusted())
	}

	claim("shop.example.com", uploaded)
	if got := trustedBy(t, w); !slices.Contains(got, zonePull) {
		t.Errorf("the shielded balancer trusts %v once shop.example.com dropped the zone's first certificate, want it kept until www.example.com, whose own claim carried it, is claimed again: another zone may still present it", got)
	}
	claim("www.example.com", uploaded)
	if got, want := trustedBy(t, w), slices.Sorted(slices.Values([]string{uploaded, otherZonePull})); !slices.Equal(got, want) {
		t.Errorf("the shielded balancer trusts %v once every hostname that carried the zone's first certificate was claimed without it, want %v: a deleted certificate's key opens nothing", got, want)
	}

	for _, hostname := range []string{"shop.example.org", "shop.example.com", "www.example.com"} {
		if err := routed.Disclaim(ctx, hostname); err != nil {
			t.Fatalf("Disclaim(%s): %v", hostname, err)
		}
	}
	trusted := trustedBy(t, w)
	if len(trusted) != 1 || slices.Contains([]string{zonePull, uploaded, otherZonePull}, trusted[0]) {
		t.Errorf("the shielded balancer trusts %v once every hostname was given back, want only a certificate whose key was thrown away", trusted)
	}
}

func TestTheShieldedLoadBalancerComesUpAtBootstrapTrustingNothingAClientCouldPresent(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	shielded := balancer.Shielded()
	if _, err := shielded.Bootstrap(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	trusted := trustedBy(t, w)
	if len(trusted) != 1 || trusted[0] == zonePull {
		t.Errorf("a shielded balancer bootstrapped before any claim trusts %v, want one certificate whose key was thrown away", trusted)
	}

	routed := unreconciledRouter(t, balancer)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCAs: []string{zonePull}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got := trustedBy(t, w); !slices.Equal(got, []string{zonePull}) {
		t.Errorf("the shielded balancer trusts %v after a claim, want the zone's certificate alone: the placeholder stands in only while nothing is claimed", got)
	}
}

func TestTakingTheShieldedLoadBalancerDownForgetsWhichCertificatesItTrusted(t *testing.T) {
	t.Parallel()

	balancer, _ := shielding(t)
	shielded := balancer.Shielded()
	if _, err := shielded.Bootstrap(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := shielded.Teardown(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if _, err := balancer.deps.KeyValues.Read(context.Background(), balancer.trustKey(environment.TierProduction)); !errors.Is(err, keyvalue.ErrNotFound) {
		t.Errorf("reading the trust record after teardown = %v, want %v: a teardown reclaims what its load balancer wrote", err, keyvalue.ErrNotFound)
	}
}

func TestTheShieldedLoadBalancerLeavesThePreviewWildcardToTheLoadBalancerItWasBoundOn(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierPreview)] = shieldedLoadBalancer()
	if err := balancer.rememberPreview(context.Background(), previewEntry{BaseDomain: "preview.example.com", Certificate: "certs/preview"}); err != nil {
		t.Fatal(err)
	}
	if _, err := balancer.Shielded().Bootstrap(context.Background(), environment.TierPreview); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, declared := w.declarations(ShieldedLoadBalancerStack(environment.TierPreview))[previewNEGName("preview.example.com")]; declared {
		t.Error("the shielded balancer declares the preview wildcard's network endpoint group, which the balancer it was bound on already owns under that name")
	}
}

func TestDestroyingTheALBRouterTakesDownWhatItsClaimsBound(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	routed := unreconciledRouter(t, balancer)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCAs: []string{zonePull}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := routed.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if hosts := w.hosts("ocel-alb-shielded-production-routes"); len(hosts) != 0 {
		t.Errorf("the shielded balancer still routes %v", hosts)
	}
	if !slices.Contains(w.torn(), BindingStack("shop", environment.TierProduction)) {
		t.Errorf("stacks torn down %v, want the project's binding stack: an edge in front of the router takes none of it down", w.torn())
	}
}

func TestTheALBRoutersRemovalPlanNamesTheHostRuleOfEveryHostnameAnEdgeForwardsToIt(t *testing.T) {
	t.Parallel()

	balancer, _ := shielding(t)
	var rules []string
	for _, group := range NewRouter(balancer).Hooks().Origin.PlanProjectRemoval(edge.ProjectScope{
		Slug: "shop", Tier: environment.TierProduction, Hostnames: []string{"shop.example.com"},
	}) {
		for _, change := range group.Changes {
			if change.Kind == "compute.URLMap host rule" && change.Action == edge.PlanDelete {
				rules = append(rules, change.Name)
			}
		}
	}
	if !slices.Equal(rules, []string{"shop.example.com"}) {
		t.Errorf("the alb router plans to delete host rules %v, want shop.example.com: an edge in front lists only what it forwards, and the plan must name what the load balancer takes down", rules)
	}
}

func shieldedPreviewLoadBalancer() map[string]string {
	return map[string]string{
		"address":        shieldedAddress,
		"certificateMap": "ocel-alb-shielded-preview-certs",
		"urlMap":         "ocel-alb-shielded-preview-routes",
		"notFound":       "ocel-alb-shielded-preview-notfound",
	}
}

func TestThePreviewEntryAnEdgeForwardsIsServedByTheShieldedLoadBalancerAlone(t *testing.T) {
	t.Parallel()

	balancer, w := balancing(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierPreview)] = shieldedPreviewLoadBalancer()
	ctx := context.Background()

	origin, err := NewRouter(balancer).Hooks().Origin.ClaimPreviewEntry(ctx, router.Claim{Hostname: "*.preview.example.com", Certificate: "certs/preview", ClientCAs: []string{zonePull}})
	if err != nil {
		t.Fatalf("ClaimPreviewEntry: %v", err)
	}
	if origin != (edge.Origin{Address: shieldedAddress, Certified: true}) {
		t.Errorf("ClaimPreviewEntry named origin %+v, want the shielded preview balancer at %s", origin, shieldedAddress)
	}
	if _, declared := w.declarations(ShieldedLoadBalancerStack(environment.TierPreview))[previewNEGName("preview.example.com")]; !declared {
		t.Error("the shielded preview balancer declares no network endpoint group for the wildcard, so no preview is answered through the edge")
	}
	if _, routed := w.hosts("ocel-alb-shielded-preview-routes")["*.preview.example.com"]; !routed {
		t.Errorf("the shielded balancer routes %v, want *.preview.example.com", w.hosts("ocel-alb-shielded-preview-routes"))
	}
	if _, err := balancer.Bootstrap(ctx, environment.TierPreview); err != nil {
		t.Fatalf("Bootstrap of the balancer browsers reach: %v", err)
	}
	if _, declared := w.declarations(LoadBalancerStack(environment.TierPreview))[previewNEGName("preview.example.com")]; declared {
		t.Error("the balancer browsers reach declares the wildcard too, so a preview that skips the edge is answered")
	}

	if err := NewRouter(balancer).Hooks().Origin.DisclaimPreviewEntry(ctx, "preview.example.com"); err != nil {
		t.Fatalf("DisclaimPreviewEntry: %v", err)
	}
	if _, routed := w.hosts("ocel-alb-shielded-preview-routes")["*.preview.example.com"]; routed {
		t.Error("the shielded balancer still routes *.preview.example.com once the entry was given back")
	}
}

func TestAClaimInOneZoneKeepsEveryCertificateAnotherZonesHostnamesCarry(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierPreview)] = shieldedPreviewLoadBalancer()
	routed := unreconciledRouter(t, balancer)
	ctx := context.Background()
	uploaded := mintTestCertificate("uploaded to both zones", true)
	if _, err := balancer.Shielded().trustClaim(ctx, environment.TierPreview, "*.preview.example.org", []string{uploaded, otherZonePull}); err != nil {
		t.Fatal(err)
	}
	if _, err := routed.Claim(ctx, router.Claim{Hostname: "shop.example.org", App: "web", Certificate: "certs/org", ClientCAs: []string{uploaded, otherZonePull}}); err != nil {
		t.Fatalf("Claim in example.org: %v", err)
	}
	if _, err := routed.Claim(ctx, router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/com", ClientCAs: []string{uploaded}}); err != nil {
		t.Fatalf("Claim in example.com: %v", err)
	}

	if got := trustedBy(t, w); !slices.Contains(got, otherZonePull) {
		t.Errorf("the shielded load balancer trusts %v once example.com was claimed, want example.org's own certificate still: example.org presents it, and dropping it refuses every hostname there", got)
	}
	_, preview, err := balancer.Shielded().readTrust(ctx, environment.TierPreview)
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.Hostnames["*.preview.example.org"]; !slices.Contains(got, otherZonePull) {
		t.Errorf("the preview tier holds %v for *.preview.example.org, want example.org's own certificate still", got)
	}
}

func TestAClaimTheAllowlistHasNoRoomForLeavesNoTrace(t *testing.T) {
	t.Parallel()

	balancer, _ := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	full := make([]string, maxAllowlisted)
	for i := range full {
		full[i] = mintTestCertificate(fmt.Sprintf("held %03d", i), false)
	}
	if _, err := shielded.trustClaim(ctx, environment.TierProduction, "shop.example.com", full); err != nil {
		t.Fatalf("a claim that fills the allowlist: %v", err)
	}
	_, before, err := shielded.readTrust(ctx, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := shielded.trustClaim(ctx, environment.TierProduction, "shop.example.org", []string{selfSigned}); err == nil {
		t.Fatal("a claim past the allowlist's limit was taken, want it refused")
	}
	_, after, err := shielded.readTrust(ctx, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("the refused claim left the trust record holding %d hostnames, want it as it was: the next claim would find it past the limit and be refused too", len(after.Hostnames))
	}
}

func TestAClaimWhoseRaiseOverlapsAnotherLeavesTheLoadBalancerTrustingBoth(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	routed := unreconciledRouter(t, balancer)
	ctx := context.Background()
	w.beforeNextUp(func() {
		if _, err := routed.Claim(ctx, router.Claim{Hostname: "shop.example.org", App: "web", Certificate: "certs/org", ClientCAs: []string{otherZonePull}}); err != nil {
			t.Errorf("the overlapping Claim: %v", err)
		}
	})
	if _, err := routed.Claim(ctx, router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/com", ClientCAs: []string{zonePull}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got, want := trustedBy(t, w), slices.Sorted(slices.Values([]string{zonePull, otherZonePull})); !slices.Equal(got, want) {
		t.Errorf("the shielded load balancer trusts %v after two claims raised it at once, want %v: the raise that finished last must not leave out what the other recorded", got, want)
	}
}

func TestTheShieldedLoadBalancerRedirectsPlainHTTPToHTTPSAndForwardsItNowhere(t *testing.T) {
	t.Parallel()

	seen, err := declared(loadBalancerProgram(loadBalancerSpec{Names: loadBalancerNames(environment.TierProduction, true), ClientCAs: []string{zonePull}}))
	if err != nil {
		t.Fatalf("the shielded balancer program = %v", err)
	}
	redirect := seen["ocel-alb-shielded-production-redirect"]
	if redirect.Token != "gcp:compute/uRLMap:URLMap" || redirect.Args["defaultService"] != nil || redirect.Args["hostRules"] != nil {
		t.Fatalf("the redirect url map is %+v, want a url map that names no backend: plain http carries no client certificate", redirect)
	}
	answer, _ := redirect.Args["defaultUrlRedirect"].(map[string]any)
	if answer["httpsRedirect"] != true || answer["redirectResponseCode"] != "PERMANENT_REDIRECT" || answer["stripQuery"] != false {
		t.Errorf("the redirect url map answers %v, want a 308 to the same URL over https: Cloudflare reaches the origin over plain http when the visitor did", answer)
	}
	proxy := seen["ocel-alb-shielded-production-http"]
	if proxy.Token != "gcp:compute/targetHttpProxy:TargetHttpProxy" || !strings.Contains(fmt.Sprint(proxy.Args["urlMap"]), "ocel-alb-shielded-production-redirect") {
		t.Errorf("the http proxy is %+v, want a target http proxy onto the redirect url map", proxy)
	}
	rule := seen["ocel-alb-shielded-production-forward-http"]
	https := seen["ocel-alb-shielded-production-forward"]
	if rule.Args["portRange"] != "80" || !strings.Contains(fmt.Sprint(rule.Args["target"]), "ocel-alb-shielded-production-http") || rule.Args["ipAddress"] != https.Args["ipAddress"] {
		t.Errorf("the http forwarding rule is %+v, want port 80 onto the http proxy, on the address the https rule answers", rule.Args)
	}

	plain, err := declared(loadBalancerProgram(loadBalancerSpec{Names: loadBalancerNames(environment.TierProduction, false)}))
	if err != nil {
		t.Fatalf("the balancer program = %v", err)
	}
	for name := range plain {
		if strings.HasSuffix(name, "-redirect") || strings.HasSuffix(name, "-forward-http") {
			t.Errorf("the balancer no edge proxies declares %s, want no plain http listener: nothing forwards http to it", name)
		}
	}
}

func TestAClaimPastTheTrustAnchorLimitIsRefusedNamingIt(t *testing.T) {
	t.Parallel()

	balancer, _ := shielding(t)
	shielded := balancer.Shielded()
	ctx := context.Background()
	full := make([]string, maxTrustAnchors)
	for i := range full {
		full[i] = mintTestCertificate(fmt.Sprintf("CA %03d", i), true)
	}
	if _, err := shielded.trustClaim(ctx, environment.TierProduction, "shop.example.com", full); err != nil {
		t.Fatalf("a claim that fills the trust anchors: %v", err)
	}
	_, err := shielded.trustClaim(ctx, environment.TierProduction, "shop.example.org", []string{otherZonePull})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "trust anchors") {
		t.Errorf("a claim past the trust anchor limit = %v, want it refused naming the limit", err)
	}
}
