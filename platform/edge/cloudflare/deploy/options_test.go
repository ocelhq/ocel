package cloudflare

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
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
	if NewProxy("ocel", fixedOrigin(testOrigin), Options{}).Facts().TunnelsToOrigin {
		t.Error("the proxy with no options reaches its origin through a tunnel, want at its address")
	}
	if !NewProxy("ocel", fixedOrigin(testOrigin), Options{Tunnel: true}).Facts().TunnelsToOrigin {
		t.Error("the proxy set to tunnel reaches its origin at its address, want through a tunnel")
	}
	if !New("ocel", fixedOrigin(testOrigin), Options{Tunnel: true}).Facts().TunnelsToOrigin {
		t.Error("the worker edge set to tunnel reports no tunnel, and the provider must see it to refuse one it cannot run")
	}
}

func TestOptionsDecodeCache(t *testing.T) {
	decoded, err := DecodeOptions(provider.Options{"cache": true})
	if err != nil || !decoded.Cache {
		t.Fatalf("DecodeOptions(cache) = %+v, %v; want cache set", decoded, err)
	}
}

func TestOptionsDecodeTunnelAndRefuseAKeyTheEdgeDoesNotTake(t *testing.T) {
	decoded, err := DecodeOptions(provider.Options{"tunnel": true})
	if err != nil || !decoded.Tunnel {
		t.Fatalf("DecodeOptions(tunnel) = %+v, %v; want tunnel set", decoded, err)
	}
	_, err = DecodeOptions(provider.Options{"shield": true})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnknownOption || !strings.Contains(err.Error(), "edge.cloudflare.shield") {
		t.Errorf("DecodeOptions(shield) error = %v, want an unknown-option refusal naming edge.cloudflare.shield", err)
	}
}
