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
			t.Fatalf("%s is a %s the inventory has no name for", name, resource.Token)
		}
		counts[tf]++
	}
	return counts
}

func listedBalancer(t *testing.T, tier environment.Tier) []pricing.Item {
	t.Helper()
	inventory, err := Inventory("ocel", pricing.EdgeSite{Tier: tier})
	if err != nil {
		t.Fatal(err)
	}
	return inventory.Shared
}

func listedHosts(t *testing.T, tier environment.Tier, hostnames ...string) []pricing.Item {
	t.Helper()
	listed, err := HostsInventory("ocel", "shop", tier, hostnames)
	if err != nil {
		t.Fatal(err)
	}
	return listed
}

func listedCounts(listed []pricing.Item) map[string]int {
	counts := map[string]int{}
	for _, s := range listed {
		counts[s.Type]++
	}
	return counts
}

func TestTheLoadBalancerInventoryMatchesItsProgram(t *testing.T) {
	t.Parallel()

	spec := loadBalancerSpec{Namespace: "ocel", Region: "europe-west1", Names: loadBalancerNames("ocel", environment.TierPreview),
		Assets: &tierAssets, Preview: previewEntry{BaseDomain: "preview.example.com", Certificate: "cert"}}
	registered := declaredCounts(t, must(loadBalancerProgram(spec)))
	maps.Copy(registered, declaredCounts(t, gateProgram(gateSpec{Names: spec.Names, Main: newGateVersion("image", "config")})))
	listed := listedCounts(listedBalancer(t, environment.TierPreview))
	if !maps.Equal(registered, listed) {
		t.Errorf("the balancer and gate programs register %v, the inventory lists %v", registered, listed)
	}

	registered = declaredCounts(t, must(loadBalancerProgram(frontSpec())))
	maps.Copy(registered, declaredCounts(t, gateProgram(gateSpec{Names: frontSpec().Names, Main: newGateVersion("image", "config")})))
	listed = listedCounts(listedBalancer(t, environment.TierProduction))
	if !maps.Equal(registered, listed) {
		t.Errorf("without a preview base the balancer program registers %v, the inventory lists %v", registered, listed)
	}
	for _, s := range listedBalancer(t, environment.TierProduction) {
		if s.Type == tfGlobalForwardingRule && s.Properties["network_tier"] != premiumTier {
			t.Errorf("forwarding rule = %v, want the premium tier the program asks for", s.Properties)
		}
	}
}

func TestAPreviewTiersLoadBalancerCachesAndServesStaticFilesFromABackendBucketLikeProduction(t *testing.T) {
	t.Parallel()

	balancer := listedBalancer(t, environment.TierPreview)
	if listedCounts(balancer)[tfBackendBucket] != 1 {
		t.Errorf("the preview tier's balancer inventory lists %v, want the assets backend bucket production has", listedCounts(balancer))
	}
	for _, listed := range listedHosts(t, environment.TierPreview, "pr-7.preview.example.com") {
		if listed.Type == tfBackendService && listed.Properties["enable_cdn"] != true {
			t.Errorf("%s = %v, want Cloud CDN on: a preview is cached like production", listed.Name, listed.Properties)
		}
	}
}

func TestTheHostInventoryMatchesTheBindingProgram(t *testing.T) {
	t.Parallel()

	hosts := map[string]Host{
		"shop.example.com":  {Certificate: "cert", Service: "svc-a", Backend: must(backendName("ocel", "shop", environment.TierProduction, "shop.example.com"))},
		"admin.example.com": {Certificate: "cert", Service: "svc-b", Backend: must(backendName("ocel", "shop", environment.TierProduction, "admin.example.com"))},
	}
	registered := declaredCounts(t, must(binding(hosts)))
	listed := listedCounts(listedHosts(t, environment.TierProduction, "shop.example.com", "admin.example.com"))
	if !maps.Equal(registered, listed) {
		t.Errorf("the binding program registers %v, the inventory lists %v", registered, listed)
	}
	for _, s := range listedHosts(t, environment.TierProduction, "shop.example.com") {
		if s.Type == tfBackendService && s.Properties["enable_cdn"] != true {
			t.Errorf("a host's backend = %v, want the CDN the program enables", s.Properties)
		}
	}
}

func TestEveryTiersBalancerListsTheGatePlugin(t *testing.T) {
	t.Parallel()

	inventories := map[environment.Tier]map[string]int{
		environment.TierPreview:    listedCounts(listedBalancer(t, environment.TierPreview)),
		environment.TierProduction: listedCounts(listedBalancer(t, environment.TierProduction)),
	}
	for tier, inventory := range inventories {
		for _, typ := range []string{tfWasmPlugin, tfLBEdgeExtension} {
			if inventory[typ] != 1 {
				t.Errorf("the %s tier's balancer inventory lists %v, want one %s: the gate runs as a plugin on it", tier, inventory, typ)
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
