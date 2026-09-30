package cloudflare

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
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
