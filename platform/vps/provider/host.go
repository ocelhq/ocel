package vps

import (
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/component"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (p *Provider) onHost(dial ssh.Dial) *Provider {
	box := ssh.NewBox(dial, nil)
	p.host = host.New(box, host.Keys{Path: p.options.DeployKey}, pins(p.options.Certificates), p.options.Proxy.Filled())
	p.routing = p.host.Routing()
	p.tunnel = p.host.Tunnel()
	p.pins = p.host.Pins()
	p.containers = p.host.Containers()
	p.images = p.host.Images()
	p.ssh = box
	p.resources = p.host.Resources()
	p.keyValues = boxstore.NewKeyValues(box)
	p.cipher = boxstore.NewCipher(box)
	p.Loopback = p.servedOnTheBox
	p.IsLoopbackOnly = p.isAnsweredOnlyOnTheBox
	return p
}

func (p *Provider) buckets() bucket.Resource { return bucket.New(p.components(), &p.stores) }

func (p *Provider) components() component.Box {
	return component.Box{
		SSH:        p.ssh,
		Containers: p.resources,
		Apps:       p.containers,
		Routing:    p.routing,
		KeyValues:  p.keyValues,
		Cipher:     p.cipher,
		Transform:  p.transform,
	}
}
