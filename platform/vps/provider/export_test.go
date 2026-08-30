package vps

import "github.com/ocelhq/ocel/platform/vps/provider/host"

var ProviderOver = newProvider

func (p *Provider) Host() *host.Host { return p.host }
