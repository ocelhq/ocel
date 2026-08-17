// Package origin — PROTOTYPE: the origin's edge registry; would live under platform/aws/provider.
package origin

import (
	"fmt"
	"sort"

	"github.com/ocelhq/ocel/platform/edge/contract/prototype/cloudflare"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/edge"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/native"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/none"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/origin/ledger"
)

type Deps struct {
	CloudFront any
	KVS        any
	APIGateway any
	Ledger     ledger.Dynamo
}

var supported = map[edge.Kind]func(Deps) edge.Edge{
	edge.KindCloudflare: func(Deps) edge.Edge { return cloudflare.New() },
	edge.KindNative: func(d Deps) edge.Edge {
		return native.New(native.Deps{CloudFront: d.CloudFront, KVS: d.KVS, Ledger: d.Ledger})
	},
	edge.KindNone: func(d Deps) edge.Edge {
		return none.New(none.Deps{APIGateway: d.APIGateway, Ledger: d.Ledger})
	},
}

func SupportedEdges() []edge.Kind {
	kinds := make([]edge.Kind, 0, len(supported))
	for k := range supported {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// EdgeFor is pull-by-kind: the CLI names the kind, the origin refuses a pairing it lacks.
func EdgeFor(kind edge.Kind, deps Deps) (edge.Edge, error) {
	ctor, ok := supported[kind]
	if !ok {
		return nil, fmt.Errorf("aws does not support edge %q (supported: %v)", kind, SupportedEdges())
	}
	return ctor(deps), nil
}
