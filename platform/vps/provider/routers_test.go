//go:build integration

package vps_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/router"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func routed(t *testing.T, p *vps.Provider, stack edge.EdgeStack) router.Stack {
	t.Helper()
	paired, err := p.Routers().Open(switchboard.RouterKind)
	if err != nil {
		t.Fatalf("Routers().Open(%q) = %v", switchboard.RouterKind, err)
	}
	opened, err := paired.Open(router.NewStackState(stack.State()))
	if err != nil {
		t.Fatalf("Open the router = %v", err)
	}
	return opened
}
