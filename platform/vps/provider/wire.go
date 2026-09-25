package vps

import (
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (p *Provider) wire(dial host.Dial) *Provider {
	p.host = host.New(dial, host.Keys{Path: p.options.DeployKey}, pins(p.options.Certificates), p.options.Proxy.front())
	p.records = host.NewRecords(p.host)
	p.cipher = host.NewCipher(p.host)
	p.Loopback = p.servedOnTheBox
	p.LoopbackOnly = !p.host.FrontProxy().Guarantees().OwnsPorts
	return p
}
