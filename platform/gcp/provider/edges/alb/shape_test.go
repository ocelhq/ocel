package alb

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
)

var pulumiTokens = map[string]string{
	"gcp:compute/globalAddress:GlobalAddress":                           tfGlobalAddress,
	"gcp:compute/backendService:BackendService":                         tfBackendService,
	"gcp:certificatemanager/certificateMap:CertificateMap":              tfCertificateMap,
	"gcp:certificatemanager/certificateMapEntry:CertificateMapEntry":    tfCertificateMapEntry,
	"gcp:compute/uRLMap:URLMap":                                         tfURLMap,
	"gcp:compute/targetHttpsProxy:TargetHttpsProxy":                     tfTargetHTTPSProxy,
	"gcp:compute/globalForwardingRule:GlobalForwardingRule":             tfGlobalForwardingRule,
	"gcp:compute/regionNetworkEndpointGroup:RegionNetworkEndpointGroup": tfNetworkEndpointGroup,
	"gcp:compute/backendBucket:BackendBucket":                           tfBackendBucket,
	"gcp:projects/iAMMember:IAMMember":                                  tfProjectIAMMember,
	"gcp:networkservices/wasmPlugin:WasmPlugin":                         tfWasmPlugin,
	"gcp:networkservices/lbEdgeExtension:LbEdgeExtension":               tfLBEdgeExtension,
}

func declaredCounts(t *testing.T, program Program) map[string]int {
	t.Helper()
	seen, err := declared(program)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for name, resource := range seen {
		tf, known := pulumiTokens[resource.Token]
		if !known {
			t.Fatalf("%s is a %s the shape has no name for", name, resource.Token)
		}
		counts[tf]++
	}
	return counts
}

func shapedBalancer(t *testing.T, tier environment.Tier) []pricing.Shaped {
	t.Helper()
	shape, err := Shape("ocel", pricing.EdgeSite{Tier: tier})
	if err != nil {
		t.Fatal(err)
	}
	return shape.Shared
}

func shapedHosts(t *testing.T, tier environment.Tier, hostnames ...string) []pricing.Shaped {
	t.Helper()
	shaped, err := ShapeHosts("ocel", "shop", tier, hostnames)
	if err != nil {
		t.Fatal(err)
	}
	return shaped
}

func shapedCounts(shaped []pricing.Shaped) map[string]int {
	counts := map[string]int{}
	for _, s := range shaped {
		counts[s.Type]++
	}
	return counts
}

func TestTheLoadBalancerShapeMatchesItsProgram(t *testing.T) {
	t.Parallel()

	spec := loadBalancerSpec{Namespace: "ocel", Region: "europe-west1", Names: loadBalancerNames("ocel", environment.TierPreview, false),
		Assets: &tierAssets, Preview: previewEntry{BaseDomain: "preview.example.com", Certificate: "cert"}}
	registered := declaredCounts(t, loadBalancerProgram(spec))
	maps.Copy(registered, declaredCounts(t, gateProgram(gateSpec{Names: spec.Names, Main: newGateVersion("image", "config")})))
	shaped := shapedCounts(shapedBalancer(t, environment.TierPreview))
	if !maps.Equal(registered, shaped) {
		t.Errorf("the balancer and gate programs register %v, the shape lists %v", registered, shaped)
	}

	registered = declaredCounts(t, loadBalancerProgram(frontSpec()))
	maps.Copy(registered, declaredCounts(t, gateProgram(gateSpec{Names: frontSpec().Names, Main: newGateVersion("image", "config")})))
	shaped = shapedCounts(shapedBalancer(t, environment.TierProduction))
	if !maps.Equal(registered, shaped) {
		t.Errorf("without a preview base the balancer program registers %v, the shape lists %v", registered, shaped)
	}
	for _, s := range shapedBalancer(t, environment.TierProduction) {
		if s.Type == tfGlobalForwardingRule && s.Properties["network_tier"] != premiumTier {
			t.Errorf("forwarding rule = %v, want the premium tier the program asks for", s.Properties)
		}
	}
}

func TestAPreviewTiersLoadBalancerCachesAndServesStaticFilesFromABackendBucketLikeProduction(t *testing.T) {
	t.Parallel()

	balancer := shapedBalancer(t, environment.TierPreview)
	if shapedCounts(balancer)[tfBackendBucket] != 1 {
		t.Errorf("the preview tier's balancer shape lists %v, want the assets backend bucket production has", shapedCounts(balancer))
	}
	for _, shaped := range shapedHosts(t, environment.TierPreview, "pr-7.preview.example.com") {
		if shaped.Type == tfBackendService && shaped.Properties["enable_cdn"] != true {
			t.Errorf("%s = %v, want Cloud CDN on: a preview is cached like production", shaped.Name, shaped.Properties)
		}
	}
}

func TestTheHostShapeMatchesTheBindingProgram(t *testing.T) {
	t.Parallel()

	hosts := map[string]Host{
		"shop.example.com":  {Certificate: "cert", Service: "svc-a", Backend: must(backendName("ocel", "shop", environment.TierProduction, "shop.example.com"))},
		"admin.example.com": {Certificate: "cert", Service: "svc-b", Backend: must(backendName("ocel", "shop", environment.TierProduction, "admin.example.com"))},
	}
	registered := declaredCounts(t, binding(hosts))
	shaped := shapedCounts(shapedHosts(t, environment.TierProduction, "shop.example.com", "admin.example.com"))
	if !maps.Equal(registered, shaped) {
		t.Errorf("the binding program registers %v, the shape lists %v", registered, shaped)
	}
	for _, s := range shapedHosts(t, environment.TierProduction, "shop.example.com") {
		if s.Type == tfBackendService && s.Properties["enable_cdn"] != true {
			t.Errorf("a host's backend = %v, want the CDN the program enables", s.Properties)
		}
	}
}

func TestEveryTiersBalancerShapesTheGatePlugin(t *testing.T) {
	t.Parallel()

	shapes := map[environment.Tier]map[string]int{
		environment.TierPreview:    shapedCounts(shapedBalancer(t, environment.TierPreview)),
		environment.TierProduction: shapedCounts(shapedBalancer(t, environment.TierProduction)),
	}
	for tier, shape := range shapes {
		for _, typ := range []string{tfWasmPlugin, tfLBEdgeExtension} {
			if shape[typ] != 1 {
				t.Errorf("the %s tier's balancer shape lists %v, want one %s: the gate runs as a plugin on it", tier, shape, typ)
			}
		}
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
