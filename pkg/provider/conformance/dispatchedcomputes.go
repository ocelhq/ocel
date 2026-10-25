package conformance

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func ListDispatchedComputes(t *testing.T, p provider.Provider, front edge.Kind) []provider.Compute {
	t.Helper()

	opened, err := p.Edges().Open(front, nil)
	if err != nil {
		t.Fatalf("Edges().Open(%s) = %v", front, err)
	}
	dispatched, err := providerserver.ListDispatchedComputes(p, opened)
	if err != nil {
		t.Fatalf("ListDispatchedComputes(%s) = %v", front, err)
	}
	return dispatched
}
