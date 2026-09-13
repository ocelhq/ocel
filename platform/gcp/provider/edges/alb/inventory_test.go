package alb

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pkg/providerkit"
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
}

func declaredCounts(t *testing.T, program Program) map[string]int {
	t.Helper()
	seen, err := declared(program)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for name, held := range seen {
		tf, known := pulumiTokens[held.Token]
		if !known {
			t.Fatalf("%s is a %s the inventory has no type for", name, held.Token)
		}
		counts[tf]++
	}
	return counts
}

func itemCounts(items []costkit.Item) map[string]int {
	counts := map[string]int{}
	for _, s := range items {
		counts[s.Type]++
	}
	return counts
}

func TestTheFrontInventoryMatchesTheFrontProgram(t *testing.T) {
	t.Parallel()

	spec := frontSpec{Region: "europe-west1", Names: frontNames(providerkit.ClassPreview),
		Preview: previewEntry{BaseDomain: "preview.example.com", Certificate: "cert"}}
	registered := declaredCounts(t, frontProgram(spec))
	listed := itemCounts(InventoryFront(providerkit.ClassPreview, "preview.example.com"))
	if !maps.Equal(registered, listed) {
		t.Errorf("the front program registers %v, the inventory lists %v", registered, listed)
	}

	registered = declaredCounts(t, frontProgram(frontSpec{Names: frontNames(providerkit.ClassProduction)}))
	listed = itemCounts(InventoryFront(providerkit.ClassProduction, ""))
	if !maps.Equal(registered, listed) {
		t.Errorf("without a preview base the front program registers %v, the inventory lists %v", registered, listed)
	}
	for _, s := range InventoryFront(providerkit.ClassProduction, "") {
		if s.Type == tfGlobalForwardingRule && s.Properties["network_tier"] != premiumTier {
			t.Errorf("forwarding rule = %v, want the premium tier the program asks for", s.Properties)
		}
	}
}

func TestTheHostInventoryMatchesTheBindingProgram(t *testing.T) {
	t.Parallel()

	hosts := map[string]Host{
		"shop.example.com":  {Certificate: "cert", Service: "svc-a", Backend: backendName("shop", providerkit.ClassProduction, "shop.example.com")},
		"admin.example.com": {Certificate: "cert", Service: "svc-b", Backend: backendName("shop", providerkit.ClassProduction, "admin.example.com")},
	}
	registered := declaredCounts(t, binding(hosts))
	listed := itemCounts(InventoryHosts("shop", providerkit.ClassProduction, []string{"shop.example.com", "admin.example.com"}))
	if !maps.Equal(registered, listed) {
		t.Errorf("the binding program registers %v, the inventory lists %v", registered, listed)
	}
	for _, s := range InventoryHosts("shop", providerkit.ClassProduction, []string{"shop.example.com"}) {
		if s.Type == tfBackendService && s.Properties["enable_cdn"] != true {
			t.Errorf("a host's backend = %v, want the CDN the program enables", s.Properties)
		}
	}
}
