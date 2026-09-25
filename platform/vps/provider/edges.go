package vps

import (
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

type edges struct{ provider *Provider }

func (e edges) Open(kind edge.Kind) (edge.Edge, error) {
	if kind != box.Kind {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"edge %q is not supported; use %q", kind, box.Kind)
	}
	return e.provider.box(), nil
}

func (p *Provider) box() *box.Edge {
	return box.New(p.host, p.holdOrigins, p.records, p.options.SSH.session().Destination())
}
