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
	balancer, w := fronting(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierProduction)] = shieldedFront()
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
	var pems []string
	for _, entry := range trust.Args["allowlistedCertificates"].([]any) {
		pems = append(pems, entry.(map[string]any)["pemCertificate"].(string))
	}
	slices.Sort(pems)
	return pems
}

func TestTheShieldedFrontRefusesEveryConnectionThatPresentsNoCertificateItTrusts(t *testing.T) {
	t.Parallel()

	seen, err := declared(loadBalancerProgram(loadBalancerSpec{Names: loadBalancerNames(environment.TierProduction, true), ClientCertificates: []string{zonePull}}))
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

func TestAClaimWithAClientCertificateAnswersTheHostnameOnTheShieldedFrontAndNamesItAsTheOrigin(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	routed := unreconciledRouter(t, balancer)

	origin, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCertificates: []string{zonePull}})
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

func TestTheShieldedFrontIsRaisedAgainOnlyForACertificateItDoesNotYetTrust(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	routed := unreconciledRouter(t, balancer)
	ctx := context.Background()
	for _, hostname := range []string{"shop.example.com", "www.example.com"} {
		if _, err := routed.Claim(ctx, router.Claim{Hostname: hostname, App: "web", Certificate: "certs/" + hostname, ClientCertificates: []string{zonePull}}); err != nil {
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

	if _, err := routed.Claim(ctx, router.Claim{Hostname: "shop.example.org", App: "web", Certificate: "certs/org", ClientCertificates: []string{otherZonePull}}); err != nil {
		t.Fatalf("Claim in another zone: %v", err)
	}
	if got := raised(); got != 2 {
		t.Errorf("the shielded balancer was raised %d times, want again for another zone's certificate", got)
	}
	if got, want := trustedBy(t, w), []string{zonePull, otherZonePull}; !slices.Equal(got, want) {
		t.Errorf("the shielded balancer trusts %v, want both zones' certificates: every project forwarded through it keeps answering", got)
	}
}

func TestTheShieldedFrontTrustsExactlyWhatTheHostnamesClaimedOnItCarry(t *testing.T) {
	t.Parallel()

	balancer, w := shielding(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierPreview)] = shieldedFront()
	routed := unreconciledRouter(t, balancer)
	ctx := context.Background()
	claim := func(hostname string, certificates ...string) {
		t.Helper()
		if _, err := routed.Claim(ctx, router.Claim{Hostname: hostname, App: "web", Certificate: "certs/" + hostname, ClientCertificates: certificates}); err != nil {
			t.Fatalf("Claim(%s): %v", hostname, err)
		}
	}
	const uploaded = "-----BEGIN CERTIFICATE-----\nzone one, uploaded beside it\n-----END CERTIFICATE-----\n"
	claim("shop.example.com", zonePull)
	claim("www.example.com", zonePull)
	claim("shop.example.org", otherZonePull)
	if _, err := balancer.Shielded().trustClaim(ctx, environment.TierPreview, "*.preview.example.com", []string{zonePull}); err != nil {
		t.Fatal(err)
	}

	claim("shop.example.com", zonePull, uploaded)
	if got, want := trustedBy(t, w), []string{zonePull, uploaded, otherZonePull}; !slices.Equal(got, want) {
		t.Errorf("the shielded balancer trusts %v once you uploaded a second certificate to the zone, want %v", got, want)
	}
	_, preview, err := balancer.Shielded().readTrust(ctx, environment.TierPreview)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(preview.allowlist(), uploaded) {
		t.Errorf("the preview tier's shielded balancer trusts %v, want the uploaded certificate too: the zone presents the certificate uploaded last to every origin it forwards to", preview.allowlist())
	}

	claim("shop.example.com", uploaded)
	if got, want := trustedBy(t, w), []string{uploaded, otherZonePull}; !slices.Equal(got, want) {
		t.Errorf("the shielded balancer trusts %v once you deleted the zone's first certificate, want %v: a deleted certificate's key opens nothing", got, want)
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

func TestTheShieldedFrontComesUpAtBootstrapTrustingNothingAClientCouldPresent(t *testing.T) {
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
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCertificates: []string{zonePull}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got := trustedBy(t, w); !slices.Equal(got, []string{zonePull}) {
		t.Errorf("the shielded balancer trusts %v after a claim, want the zone's certificate alone: the placeholder stands in only while nothing is claimed", got)
	}
}

func TestTheShieldedFrontLeavesThePreviewWildcardToTheFrontItWasBoundOn(t *testing.T) {
	t.Parallel()

	balancer, w := fronting(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierPreview)] = shieldedFront()
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
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", Certificate: "certs/shop", ClientCertificates: []string{zonePull}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := routed.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if hosts := w.hosts("ocel-alb-shielded-production-routes"); len(hosts) != 0 {
		t.Errorf("the shielded balancer still routes %v", hosts)
	}
	if !slices.Contains(w.torn(), BindingStack("shop", environment.TierProduction)) {
		t.Errorf("stacks torn down %v, want the project's binding stack: an edge in balancer of the router takes none of it down", w.torn())
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
		t.Errorf("the alb router plans to delete host rules %v, want shop.example.com: an edge in balancer lists only what it forwards, and the plan must name what the load balancer takes down", rules)
	}
}

func shieldedPreviewFront() map[string]string {
	return map[string]string{
		"address":        shieldedAddress,
		"certificateMap": "ocel-alb-shielded-preview-certs",
		"urlMap":         "ocel-alb-shielded-preview-routes",
		"notFound":       "ocel-alb-shielded-preview-notfound",
	}
}

func TestThePreviewEntryAnEdgeForwardsIsServedByTheShieldedFrontAlone(t *testing.T) {
	t.Parallel()

	balancer, w := fronting(t)
	w.outputs[ShieldedLoadBalancerStack(environment.TierPreview)] = shieldedPreviewFront()
	ctx := context.Background()

	origin, err := NewRouter(balancer).Hooks().Origin.ClaimPreviewEntry(ctx, router.Claim{Hostname: "*.preview.example.com", Certificate: "certs/preview", ClientCertificates: []string{zonePull}})
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
