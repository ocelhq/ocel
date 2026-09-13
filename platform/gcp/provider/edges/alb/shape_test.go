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
			t.Fatalf("%s is a %s the shape has no name for", name, held.Token)
		}
		counts[tf]++
	}
	return counts
}

func shapedCounts(shaped []costkit.Shaped) map[string]int {
	counts := map[string]int{}
	for _, s := range shaped {
		counts[s.Type]++
	}
	return counts
}

func TestTheFrontShapeMatchesTheFrontProgram(t *testing.T) {
	t.Parallel()

	spec := frontSpec{Region: "europe-west1", Names: frontNames(providerkit.ClassPreview),
		Preview: previewEntry{BaseDomain: "preview.example.com", Certificate: "cert"}}
	registered := declaredCounts(t, frontProgram(spec))
	shaped := shapedCounts(ShapeFront(providerkit.ClassPreview, "preview.example.com"))
	if !maps.Equal(registered, shaped) {
		t.Errorf("the front program registers %v, the shape lists %v", registered, shaped)
	}

	registered = declaredCounts(t, frontProgram(frontSpec{Names: frontNames(providerkit.ClassProduction)}))
	shaped = shapedCounts(ShapeFront(providerkit.ClassProduction, ""))
	if !maps.Equal(registered, shaped) {
		t.Errorf("without a preview base the front program registers %v, the shape lists %v", registered, shaped)
	}
	for _, s := range ShapeFront(providerkit.ClassProduction, "") {
		if s.Type == tfGlobalForwardingRule && s.Properties["network_tier"] != premiumTier {
			t.Errorf("forwarding rule = %v, want the premium tier the program asks for", s.Properties)
		}
	}
}

func TestTheHostShapeMatchesTheBindingProgram(t *testing.T) {
	t.Parallel()

	hosts := map[string]Host{
		"shop.example.com":  {Certificate: "cert", Service: "svc-a", Backend: backendName("shop", providerkit.ClassProduction, "shop.example.com")},
		"admin.example.com": {Certificate: "cert", Service: "svc-b", Backend: backendName("shop", providerkit.ClassProduction, "admin.example.com")},
	}
	registered := declaredCounts(t, binding(hosts))
	shaped := shapedCounts(ShapeHosts("shop", providerkit.ClassProduction, []string{"shop.example.com", "admin.example.com"}))
	if !maps.Equal(registered, shaped) {
		t.Errorf("the binding program registers %v, the shape lists %v", registered, shaped)
	}
	for _, s := range ShapeHosts("shop", providerkit.ClassProduction, []string{"shop.example.com"}) {
		if s.Type == tfBackendService && s.Properties["enable_cdn"] != true {
			t.Errorf("a host's backend = %v, want the CDN the program enables", s.Properties)
		}
	}
}
