package alb

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func binding(hosts map[string]Host) Program {
	return shieldedBinding(hosts, false)
}

func shieldedBinding(hosts map[string]Host, shielded bool) Program {
	return bindingProgram(bindingSpec{
		Namespace:      "ocel",
		Shielded:       shielded,
		Region:         "europe-west1",
		Slug:           "shop",
		Tier:           environment.TierProduction,
		CertificateMap: "ocel-prod-alb-certs",
		Hosts:          hosts,
	})
}

func TestTheFrontendProvisionsOneLoadBalancerForTheWholeTier(t *testing.T) {
	t.Parallel()

	seen, err := declared(loadBalancerProgram(frontSpec()))
	if err != nil {
		t.Fatalf("the frontend program = %v", err)
	}

	for name, token := range map[string]string{
		"ocel-prod-alb-address":  "gcp:compute/globalAddress:GlobalAddress",
		"ocel-prod-alb-notfound": "gcp:compute/backendService:BackendService",
		"ocel-prod-alb-certs":    "gcp:certificatemanager/certificateMap:CertificateMap",
		"ocel-prod-alb-routes":   "gcp:compute/uRLMap:URLMap",
		"ocel-prod-alb-https":    "gcp:compute/targetHttpsProxy:TargetHttpsProxy",
		"ocel-prod-alb-forward":  "gcp:compute/globalForwardingRule:GlobalForwardingRule",
		"ocel-prod-alb-assets":   "gcp:compute/backendBucket:BackendBucket",
		"ocel-prod-alb-reader":   "gcp:projects/iAMMember:IAMMember",
	} {
		resource, declared := seen[name]
		if !declared {
			t.Errorf("the frontend declares no %s; without it a hostname on this tier reaches nothing", name)
			continue
		}
		if resource.Token != token {
			t.Errorf("%s is a %s, want a %s", name, resource.Token, token)
		}
	}
	if len(seen) != 8 {
		t.Errorf("the frontend declares %d resources, want the six one load balancer is and the backend bucket with the grant that lets it read: %v", len(seen), seen)
	}

	routes := seen["ocel-prod-alb-routes"]
	if got := routes.Args["defaultService"]; got == nil {
		t.Error("the url map names no default service, and a hostname no project claimed would reach whichever backend answered first")
	}
	if got := abortedBy(routes.Args["defaultRouteAction"]); got != float64(notFoundStatus) {
		t.Errorf("the url map answers an unclaimed hostname with %v, want %d: the not-found backend it defaults to has no backends, "+
			"so anything that reaches it is answered 502 rather than refused", got, notFoundStatus)
	}
	if got := routes.Args["hostRules"]; got != nil {
		t.Errorf("the url map declares host rules %v, and a bind writes them: the stack that owns the map must leave them alone", got)
	}
	rule := seen["ocel-prod-alb-forward"]
	if got := rule.Args["portRange"]; got != "443" {
		t.Errorf("the forwarding rule listens on %v, want 443: the balancer terminates TLS", got)
	}
	if got := rule.Args["loadBalancingScheme"]; got != "EXTERNAL_MANAGED" {
		t.Errorf("the forwarding rule is %v, want EXTERNAL_MANAGED, which is the global external Application Load Balancer", got)
	}
	proxy := seen["ocel-prod-alb-https"]
	if got := proxy.Args["certificateMap"]; got == nil {
		t.Error("the https proxy names no certificate map, and every hostname's certificate is served through one")
	}
}

func frontSpec() loadBalancerSpec {
	return loadBalancerSpec{
		Namespace: "ocel",
		Names:     loadBalancerNames("ocel", environment.TierProduction, false),
		Assets:    &tierAssets,
	}
}

func TestTheFrontendServesTheTiersAssetsFromACachedBackendBucket(t *testing.T) {
	t.Parallel()

	seen, err := declared(loadBalancerProgram(frontSpec()))
	if err != nil {
		t.Fatalf("the frontend program = %v", err)
	}

	bucket := seen["ocel-prod-alb-assets"]
	if got := bucket.Args["bucketName"]; got != "ocel-prod-edge-k3x9q2" {
		t.Errorf("the backend bucket reads %v, want the tier's own bucket", got)
	}
	if got := bucket.Args["enableCdn"]; got != true {
		t.Errorf("the backend bucket has cdn = %v, want it on: a file served from the bucket is cached at Google's edge", got)
	}
	policy, _ := bucket.Args["cdnPolicy"].(map[string]any)
	if policy["cacheMode"] != "USE_ORIGIN_HEADERS" {
		t.Errorf("the backend bucket caches by %v, want USE_ORIGIN_HEADERS: the objects carry the cache control the router answers them with", policy["cacheMode"])
	}
	if policy["negativeCaching"] != false {
		t.Errorf("the backend bucket has negative caching = %v, want it off: a request that beats its release's upload must not be refused for minutes", policy["negativeCaching"])
	}
}

func TestTheLoadBalancersServiceAgentListsTheTiersEdgeBucketAndReadsOnlyItsStaticFiles(t *testing.T) {
	t.Parallel()

	seen, err := declared(loadBalancerProgram(frontSpec()))
	if err != nil {
		t.Fatalf("the frontend program = %v", err)
	}

	grant := seen["ocel-prod-alb-reader"]
	if got := grant.Args["member"]; got != "serviceAccount:service-4242@https-lb.iam.gserviceaccount.com" {
		t.Errorf("the grant goes to %v, want the load balancer's service agent of project number 4242", got)
	}
	if got := grant.Args["role"]; got != "roles/storage.objectViewer" {
		t.Errorf("the grant is %v, want roles/storage.objectViewer and nothing that writes", got)
	}
	condition, _ := grant.Args["condition"].(map[string]any)
	expression, _ := condition["expression"].(string)
	want := `resource.name == "projects/_/buckets/ocel-prod-edge-k3x9q2" || ` +
		`resource.name.startsWith("projects/_/buckets/ocel-prod-edge-k3x9q2/objects/assets/")`
	if expression != want {
		t.Errorf("the grant is conditioned on %q, want %q: the agent lists the edge bucket, so a missing file is a 404, "+
			"and reads its static files alone, never the pages, edge bundles, route tables or image config beside them", expression, want)
	}
}

func TestTheFrontendOfAShieldedLoadBalancerDeclaresNoBackendBucket(t *testing.T) {
	t.Parallel()

	seen, err := declared(loadBalancerProgram(loadBalancerSpec{Namespace: "ocel", Names: loadBalancerNames("ocel", environment.TierProduction, true)}))
	if err != nil {
		t.Fatalf("the frontend program = %v", err)
	}
	for name, resource := range seen {
		if resource.Token == "gcp:compute/backendBucket:BackendBucket" || resource.Token == "gcp:projects/iAMMember:IAMMember" {
			t.Errorf("the shielded frontend declares %s, a %s: a shielded host's files are served by the edge in front of it, never from a backend bucket", name, resource.Token)
		}
	}
}

func abortedBy(action any) any {
	routeAction, ok := action.(map[string]any)
	if !ok {
		return nil
	}
	policy, ok := routeAction["faultInjectionPolicy"].(map[string]any)
	if !ok {
		return nil
	}
	abort, ok := policy["abort"].(map[string]any)
	if !ok {
		return nil
	}
	return abort["httpStatus"]
}

func TestABindingDeclaresACachedBackendOnThePointersCloudRunService(t *testing.T) {
	t.Parallel()

	seen, err := declared(binding(
		map[string]Host{
			"shop.example.com": {
				App:         "web",
				Certificate: "projects/acme-prod/locations/global/certificates/shop",
				Service:     "ocel-shop-prod-web",
				Backend:     must(backendName("ocel", "shop", environment.TierProduction, "shop.example.com")),
			},
		}))
	if err != nil {
		t.Fatalf("the binding program = %v", err)
	}

	entry, declaredEntry := seen[must(backendName("ocel", "shop", environment.TierProduction, "shop.example.com"))+"-cert"]
	if !declaredEntry {
		t.Fatalf("the binding declares no certificate map entry; the balancer would serve shop.example.com no certificate. Declared: %v", seen)
	}
	if got := entry.Args["hostname"]; got != "shop.example.com" {
		t.Errorf("the certificate map entry answers for %v, want shop.example.com", got)
	}
	if got := entry.Args["map"]; got != "ocel-prod-alb-certs" {
		t.Errorf("the certificate map entry lands in %v, want the tier's own map", got)
	}

	neg, declaredNEG := seen[must(backendName("ocel", "shop", environment.TierProduction, "shop.example.com"))+"-neg"]
	if !declaredNEG {
		t.Fatalf("the binding declares no serverless neg; nothing would route a request from the load balancer to Cloud Run")
	}
	if got := neg.Args["networkEndpointType"]; got != "SERVERLESS" {
		t.Errorf("the neg is a %v, want SERVERLESS: its endpoint is a Cloud Run service, not an instance", got)
	}
	run, ok := neg.Args["cloudRun"].(map[string]any)
	if !ok || run["service"] != "ocel-shop-prod-web" {
		t.Errorf("the neg points at %v, want the Cloud Run service the pointer's active record named", neg.Args["cloudRun"])
	}

	backend, declaredBackend := seen[must(backendName("ocel", "shop", environment.TierProduction, "shop.example.com"))]
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
	if _, backed := seen[must(backendName("ocel", "shop", environment.TierProduction, "shop.example.com"))]; backed {
		t.Error("the binding declares a backend for a hostname whose app has released no service, and a neg needs one to point at")
	}
	if _, certified := seen[must(backendName("ocel", "shop", environment.TierProduction, "shop.example.com"))+"-cert"]; !certified {
		t.Error("the binding declares no certificate for a hostname it can already certify: the certificate outlives the release the backend waits on")
	}
}
