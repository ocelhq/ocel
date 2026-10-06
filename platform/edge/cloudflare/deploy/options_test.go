package cloudflare

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestEdgeSchemaIsCommitted(t *testing.T) {
	generated, err := configdoc.EdgeSchema(Kind, "Cloudflare", Options{})
	if err != nil {
		t.Fatalf("edge schema: %v", err)
	}
	schematest.AssertCommitted(t, schematest.EdgeSchemaFile, generated)
}

func TestTheEdgeReachesItsOriginThroughATunnelOnlyWhenItsOptionsAskForOne(t *testing.T) {
	if NewProxy("ocel", Options{}).Facts().TunnelsToOrigin {
		t.Error("the proxy with no options reaches its origin through a tunnel, want at its address")
	}
	if !NewProxy("ocel", Options{Tunnel: true}).Facts().TunnelsToOrigin {
		t.Error("the proxy set to tunnel reaches its origin at its address, want through a tunnel")
	}
	if !New("ocel", Options{Tunnel: true}).Facts().TunnelsToOrigin {
		t.Error("the worker edge set to tunnel reports no tunnel, and the provider must see it to refuse one it cannot run")
	}
}

func TestOptionsDecodeTunnelAndRefuseAKeyTheEdgeDoesNotTake(t *testing.T) {
	decoded, err := DecodeOptions(provider.Options{"tunnel": true})
	if err != nil || !decoded.Tunnel {
		t.Fatalf("DecodeOptions(tunnel) = %+v, %v; want tunnel set", decoded, err)
	}
	_, err = DecodeOptions(provider.Options{"cache": true})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnknownOption || !strings.Contains(err.Error(), "edge.cloudflare.cache") {
		t.Errorf("DecodeOptions(cache) error = %v, want an unknown-option refusal naming edge.cloudflare.cache", err)
	}
}

func TestTheOriginDomainIsReadAsOneLowercaseHostname(t *testing.T) {
	decoded, err := DecodeOptions(provider.Options{"originDomain": " Origin.Example.com. "})
	if err != nil || decoded.OriginDomain != "origin.example.com" {
		t.Fatalf("DecodeOptions(originDomain) = %+v, %v; want origin.example.com", decoded, err)
	}
	if empty, err := DecodeOptions(provider.Options{}); err != nil || empty.OriginDomain != "" || empty.OriginBase(environment.TierProduction) != "" {
		t.Errorf("DecodeOptions({}) = %+v, %v; want no origin domain", empty, err)
	}
}

func TestAnOriginDomainThatIsNoHostnameIsRefused(t *testing.T) {
	for _, raw := range []string{"*.o.example.com", "localhost", "o..example.com", "https://o.example.com", "o.example.com:443", "o example.com", ".example.com", strings.Repeat("a", 64) + ".example.com"} {
		_, err := DecodeOptions(provider.Options{"originDomain": raw})
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(refused.Message, "edge.cloudflare.originDomain") {
			t.Errorf("DecodeOptions(%q) = %v; want an invalid refusal naming edge.cloudflare.originDomain", raw, err)
		}
	}
	_, err := DecodeOptions(provider.Options{"originDomain": "*.o.example.com"})
	if err == nil || !strings.Contains(err.Error(), `"o.example.com"`) {
		t.Errorf("the wildcard refusal = %v; want it to name o.example.com", err)
	}
}

func TestAnOriginDomainLongerThanItsDeploymentHostnamesAllowIsRefused(t *testing.T) {
	domainOf := func(length int) string {
		label := strings.Repeat("a", 60)
		return label + "." + label + "." + strings.Repeat("b", length-2*len(label)-2-len(".com")) + ".com"
	}
	if got := domainOf(181); len(got) != 181 {
		t.Fatalf("test domain has %d characters", len(got))
	}
	if _, err := DecodeOptions(provider.Options{"originDomain": domainOf(181)}); err != nil {
		t.Errorf("a 181-character domain = %v; want it accepted", err)
	}
	_, err := DecodeOptions(provider.Options{"originDomain": domainOf(182)})
	if err == nil || !strings.Contains(err.Error(), "253") {
		t.Errorf("a 182-character domain = %v; want a refusal naming the 253-character limit", err)
	}
}

func TestThePreviewTiersOriginBaseSitsUnderTheOriginDomain(t *testing.T) {
	options := Options{OriginDomain: "o.example.com"}
	if got := options.OriginBase(environment.TierProduction); got != "o.example.com" {
		t.Errorf("production base = %q", got)
	}
	if got := options.OriginBase(environment.TierPreview); got != "preview.o.example.com" {
		t.Errorf("preview base = %q", got)
	}
}
