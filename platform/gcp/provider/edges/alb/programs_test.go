package alb

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func binding(hosts map[string]Host) Program {
	return bindingProgram(bindingSpec{
		Project:        "acme-prod",
		Region:         "europe-west1",
		Slug:           "shop",
		Class:          providerkit.ClassProduction,
		CertificateMap: "ocel-alb-production-certs",
		Hosts:          hosts,
	})
}

func TestTheFrontendStandsOneLoadBalancerUpForTheWholeClass(t *testing.T) {
	t.Parallel()

	seen, err := declared(frontProgram(frontSpec{Project: "acme-prod", Names: frontNames(providerkit.ClassProduction)}))
	if err != nil {
		t.Fatalf("the frontend program = %v", err)
	}

	for name, token := range map[string]string{
		"ocel-alb-production-address":  "gcp:compute/globalAddress:GlobalAddress",
		"ocel-alb-production-notfound": "gcp:compute/backendService:BackendService",
		"ocel-alb-production-certs":    "gcp:certificatemanager/certificateMap:CertificateMap",
		"ocel-alb-production-routes":   "gcp:compute/uRLMap:URLMap",
		"ocel-alb-production-https":    "gcp:compute/targetHttpsProxy:TargetHttpsProxy",
		"ocel-alb-production-forward":  "gcp:compute/globalForwardingRule:GlobalForwardingRule",
	} {
		held, declared := seen[name]
		if !declared {
			t.Errorf("the frontend declares no %s; without it a hostname on this class reaches nothing", name)
			continue
		}
		if held.Token != token {
			t.Errorf("%s is a %s, want a %s", name, held.Token, token)
		}
	}
	if len(seen) != 6 {
		t.Errorf("the frontend declares %d resources, want the six one load balancer is: %v", len(seen), seen)
	}

	routes := seen["ocel-alb-production-routes"]
	if got := routes.Args["defaultService"]; got == nil {
		t.Error("the url map names no default service, and a hostname no project claimed would reach whichever backend answered first")
	}
	if got := routes.Args["hostRules"]; got != nil {
		t.Errorf("the url map declares host rules %v, and a bind writes them: the stack that owns the map must leave them alone", got)
	}
	rule := seen["ocel-alb-production-forward"]
	if got := rule.Args["portRange"]; got != "443" {
		t.Errorf("the forwarding rule listens on %v, want 443: the front terminates TLS", got)
	}
	if got := rule.Args["loadBalancingScheme"]; got != "EXTERNAL_MANAGED" {
		t.Errorf("the forwarding rule is %v, want EXTERNAL_MANAGED, which is the global external Application Load Balancer", got)
	}
	proxy := seen["ocel-alb-production-https"]
	if got := proxy.Args["certificateMap"]; got == nil {
		t.Error("the https proxy names no certificate map, and every hostname's certificate is served through one")
	}
}

func TestABindingDeclaresACachedBackendOnThePointersCloudRunService(t *testing.T) {
	t.Parallel()

	seen, err := declared(binding(
		map[string]Host{
			"shop.example.com": {
				App:         "web",
				Certificate: "projects/acme-prod/locations/global/certificates/shop",
				Service:     "ocel-shop-prod-web",
				Backend:     "ocel-alb-shop-production-shop-example-com",
			},
		}))
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	entry, declaredEntry := seen["ocel-alb-shop-production-shop-example-com-cert"]
	if !declaredEntry {
		t.Fatalf("the binding declares no certificate map entry; the front would serve shop.example.com no certificate. Declared: %v", seen)
	}
	if got := entry.Args["hostname"]; got != "shop.example.com" {
		t.Errorf("the certificate map entry answers for %v, want shop.example.com", got)
	}
	if got := entry.Args["map"]; got != "ocel-alb-production-certs" {
		t.Errorf("the certificate map entry lands in %v, want the class's own map", got)
	}

	neg, declaredNEG := seen["ocel-alb-shop-production-shop-example-com-neg"]
	if !declaredNEG {
		t.Fatalf("the binding declares no serverless neg; nothing would carry a request from the load balancer to Cloud Run")
	}
	if got := neg.Args["networkEndpointType"]; got != "SERVERLESS" {
		t.Errorf("the neg is a %v, want SERVERLESS: its endpoint is a Cloud Run service, not an instance", got)
	}
	run, held := neg.Args["cloudRun"].(map[string]any)
	if !held || run["service"] != "ocel-shop-prod-web" {
		t.Errorf("the neg points at %v, want the Cloud Run service the pointer's active record named", neg.Args["cloudRun"])
	}

	backend, declaredBackend := seen["ocel-alb-shop-production-shop-example-com"]
	if !declaredBackend {
		t.Fatalf("the binding declares no backend service; Cloud CDN is a backend-service setting and there is nothing to set it on")
	}
	if got := backend.Args["enableCdn"]; got != true {
		t.Errorf("the backend has cdn = %v, want it on: the alb edge declares edge caching as a need it supports", got)
	}
	policy, hasPolicy := backend.Args["cdnPolicy"].(map[string]any)
	if !hasPolicy || policy["cacheMode"] != "USE_ORIGIN_HEADERS" {
		t.Errorf("the backend caches by %v, want USE_ORIGIN_HEADERS: the app's own cache-control decides, not a mode ocel picked", backend.Args["cdnPolicy"])
	}
}

func TestAHostnameBoundBeforeAnythingWasDeployedDeclaresNoBackendToRouteNowhere(t *testing.T) {
	t.Parallel()

	seen, err := declared(binding(
		map[string]Host{
			"shop.example.com": {Certificate: "projects/acme-prod/locations/global/certificates/shop"},
		}))
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}
	if _, backed := seen["ocel-alb-shop-production-shop-example-com"]; backed {
		t.Error("the binding declares a backend for a hostname whose app has released no service, and a neg needs one to point at")
	}
	if _, certified := seen["ocel-alb-shop-production-shop-example-com-cert"]; !certified {
		t.Error("the binding declares no certificate for a hostname it can already certify: the certificate outlives the release the backend waits on")
	}
}
