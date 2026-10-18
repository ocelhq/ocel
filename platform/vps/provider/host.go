package vps

import (
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (p *Provider) onHost(dial ssh.Dial) *Provider {
	p.host = host.New(ssh.NewBox(dial, nil), host.Keys{Path: p.options.DeployKey}, pins(p.options.Certificates), p.options.Proxy.front())
	p.keyValues = host.NewKeyValues(p.host)
	p.cipher = host.NewCipher(p.host)
	p.Loopback = p.servedOnTheBox
	p.IsLoopbackOnly = p.isAnsweredOnlyOnTheBox
	return p
}
