package vps_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/router"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
)

func routed(t *testing.T, p *vps.Provider, stack edge.EdgeStack) router.Stack {
	t.Helper()
	routes, err := p.Routers().Open(router.Kind(boxedge.Kind))
	if err != nil {
		t.Fatalf("Routers().Open(%q) = %v", boxedge.Kind, err)
	}
	state := stack.State()
	opened, err := routes.Open(router.NewStackState(state))
	if err != nil {
		t.Fatalf("Open the router = %v", err)
	}
	return opened
}
