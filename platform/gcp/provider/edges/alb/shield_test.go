package alb

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	shieldedAddress = "34.117.0.8"
	zonePull        = "-----BEGIN CERTIFICATE-----\nzone one\n-----END CERTIFICATE-----\n"
	otherZonePull   = "-----BEGIN CERTIFICATE-----\nzone two\n-----END CERTIFICATE-----\n"
)

func shieldedFront() map[string]string {
	return map[string]string{
		"address":        shieldedAddress,
		"certificateMap": "ocel-alb-shielded-production-certs",
		"urlMap":         "ocel-alb-shielded-production-routes",
		"notFound":       "ocel-alb-shielded-production-notfound",
	}
}

func shielding(t *testing.T) (*Edge, *world) {
	t.Helper()
	front, w := fronting(t)
	w.outputs[ShieldedFrontStack(environment.TierProduction)] = shieldedFront()
	return front, w
}

func unreconciledRouter(t *testing.T, front *Edge) router.Stack {
	t.Helper()
	routed, err := NewRouter(front).Open(router.NewStackState(edge.StackState{Slug: "shop", Tier: environment.TierProduction}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return routed
}

func trustedBy(t *testing.T, w *world) []string {
	t.Helper()
	trust, declared := w.declarations(ShieldedFrontStack(environment.TierProduction))["ocel-alb-shielded-production-trust"]
	if !declared {
		t.Fatal("the shielded front declares no trust config, so nothing checks the client certificate a request arrives with")
	}
	var pems []string
	for _, entry := range trust.Args["allowlistedCertificates"].([]any) {
		pems = append(pems, entry.(map[string]any)["pemCertificate"].(string))
	}
	slices.Sort(pems)
	return pems
}

func TestTheShieldedFrontRefusesEveryConnectionThatPresentsNoCertificateItTrusts(t *testing.T) {
	t.Parallel()

	seen, err := declared(frontProgram(frontSpec{Names: frontNames(environment.TierProduction, true), ClientCertificates: []string{zonePull}}))
	if err != nil {
		t.Fatalf("the shielded front program = %v", err)
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
		t.Errorf("the policy validates clients against %v, want the shielded front's trust config", got)
	}
	proxy := seen["ocel-alb-shielded-production-https"]
	if got := proxy.Args["serverTlsPolicy"]; got == nil || !strings.Contains(got.(string), "ocel-alb-shielded-production-mtls") {
		t.Errorf("the https proxy carries server tls policy %v, want the shielded front's, or it answers any client", got)
	}

	plain, err := declared(frontProgram(frontSpec{Names: frontNames(environment.TierProduction, false)}))
	if err != nil {
		t.Fatalf("the front program = %v", err)
	}
	if got := plain["ocel-alb-production-https"].Args["serverTlsPolicy"]; got != nil {
		t.Errorf("the front no edge proxies carries server tls policy %v, want none: its clients are browsers", got)
	}
}

func TestAClaimWithAClientCertificateAnswersTheHostnameOnTheShieldedFrontAndNamesItAsTheOrigin(t *testing.T) {
	t.Parallel()

	front, w := shielding(t)
	routed := unreconciledRouter(t, front)

	origin, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCertificate: zonePull})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if origin != (edge.Origin{Address: shieldedAddress}) {
		t.Errorf("Claim named origin %+v, want the shielded front at %s", origin, shieldedAddress)
	}
	if got := trustedBy(t, w); !slices.Equal(got, []string{zonePull}) {
		t.Errorf("the shielded front trusts %v, want the client certificate the claim carried", got)
	}
	if _, routedThere := w.hosts("ocel-alb-shielded-production-routes")["shop.example.com"]; !routedThere {
		t.Errorf("the shielded front's url map routes %v, want shop.example.com", w.hosts("ocel-alb-shielded-production-routes"))
	}
	if _, routedPlain := w.hosts("ocel-alb-production-routes")["shop.example.com"]; routedPlain {
		t.Error("shop.example.com is routed on the front browsers reach directly, so a request that skips the edge would be answered")
	}
	entry := w.declarations(BindingStack("shop", environment.TierProduction))[entryName("shop", environment.TierProduction, "shop.example.com")]
	if entry.Args["map"] != "ocel-alb-shielded-production-certs" {
		t.Errorf("the hostname's certificate is entered in map %v, want the shielded front's", entry.Args["map"])
	}
}

func TestTheShieldedFrontIsRaisedAgainOnlyForACertificateItDoesNotYetTrust(t *testing.T) {
	t.Parallel()

	front, w := shielding(t)
	routed := unreconciledRouter(t, front)
	ctx := context.Background()
	for _, hostname := range []string{"shop.example.com", "www.example.com"} {
		if _, err := routed.Claim(ctx, router.Claim{Hostname: hostname, App: "web", Certificate: "certs/" + hostname, ClientCertificate: zonePull}); err != nil {
			t.Fatalf("Claim(%s): %v", hostname, err)
		}
	}
	raised := func() int {
		count := 0
		for _, up := range w.raised() {
			if up == ShieldedFrontStack(environment.TierProduction) {
				count++
			}
		}
		return count
	}
	if got := raised(); got != 1 {
		t.Errorf("the shielded front was raised %d times for two hostnames in one zone, want once", got)
	}

	if _, err := routed.Claim(ctx, router.Claim{Hostname: "shop.example.org", App: "web", Certificate: "certs/org", ClientCertificate: otherZonePull}); err != nil {
		t.Fatalf("Claim in another zone: %v", err)
	}
	if got := raised(); got != 2 {
		t.Errorf("the shielded front was raised %d times, want again for another zone's certificate", got)
	}
	if got, want := trustedBy(t, w), []string{zonePull, otherZonePull}; !slices.Equal(got, want) {
		t.Errorf("the shielded front trusts %v, want both zones' certificates: every project forwarded through it keeps answering", got)
	}
}

func TestTheShieldedFrontComesUpAtBootstrapTrustingNothingAClientCouldPresent(t *testing.T) {
	t.Parallel()

	front, w := shielding(t)
	shielded := front.Shielded()
	if _, err := shielded.Bootstrap(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	trusted := trustedBy(t, w)
	if len(trusted) != 1 || trusted[0] == zonePull {
		t.Errorf("a shielded front bootstrapped before any claim trusts %v, want one certificate whose key was thrown away", trusted)
	}

	routed := unreconciledRouter(t, front)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCertificate: zonePull}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got := trustedBy(t, w); !slices.Contains(got, zonePull) {
		t.Errorf("the shielded front trusts %v after a claim, want the zone's certificate among them", got)
	}
}

func TestTheShieldedFrontLeavesThePreviewWildcardToTheFrontItWasBoundOn(t *testing.T) {
	t.Parallel()

	front, w := fronting(t)
	w.outputs[ShieldedFrontStack(environment.TierPreview)] = shieldedFront()
	if err := front.rememberPreview(context.Background(), previewEntry{BaseDomain: "preview.example.com", Certificate: "certs/preview"}); err != nil {
		t.Fatal(err)
	}
	if _, err := front.Shielded().Bootstrap(context.Background(), environment.TierPreview); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, declared := w.declarations(ShieldedFrontStack(environment.TierPreview))[previewNEGName("preview.example.com")]; declared {
		t.Error("the shielded front declares the preview wildcard's network endpoint group, which the front it was bound on already owns under that name")
	}
}

func TestDestroyingTheALBRouterTakesDownWhatItsClaimsBound(t *testing.T) {
	t.Parallel()

	front, w := shielding(t)
	routed := unreconciledRouter(t, front)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCertificate: zonePull}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := routed.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if hosts := w.hosts("ocel-alb-shielded-production-routes"); len(hosts) != 0 {
		t.Errorf("the shielded front still routes %v", hosts)
	}
	if !slices.Contains(w.torn(), BindingStack("shop", environment.TierProduction)) {
		t.Errorf("stacks torn down %v, want the project's binding stack: an edge in front of the router takes none of it down", w.torn())
	}
}
