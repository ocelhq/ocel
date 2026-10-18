package vps

import (
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (p *Provider) onHost(dial ssh.Dial) *Provider {
	box := ssh.NewBox(dial, nil)
	p.host = host.New(box, host.Keys{Path: p.options.DeployKey}, pins(p.options.Certificates), p.options.Proxy.front())
	p.keyValues = boxstore.NewKeyValues(box)
	p.cipher = boxstore.NewCipher(box)
	p.Loopback = p.servedOnTheBox
	p.IsLoopbackOnly = p.isAnsweredOnlyOnTheBox
	return p
}
