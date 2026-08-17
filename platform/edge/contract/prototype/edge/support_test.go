package edge_test

import (
	"testing"

	"github.com/ocelhq/ocel/platform/edge/contract/prototype/cloudflare"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/edge"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/native"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/none"
)

func TestProgrammableAgreesWithSupports(t *testing.T) {
	for _, e := range []edge.Edge{cloudflare.New(), native.New(native.Deps{}), none.New(none.Deps{})} {
		_, programmable := e.(edge.Programmable)
		runsCode := e.Supports(edge.EdgeMiddleware) || e.Supports(edge.EdgeRuntime)
		if programmable != runsCode {
			t.Errorf("%s: Programmable=%v but Supports(EdgeMiddleware|EdgeRuntime)=%v", e.Kind(), programmable, runsCode)
		}
	}
}
