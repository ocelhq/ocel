package control

import "github.com/ocelhq/ocel/platform/gcp/provider/assets"

type artifacts struct {
	p *Provider
}

func (p *Provider) Objects() *assets.Store {
	return &assets.Store{OpenClients: p.OpenClients, RemoveTagRecords: artifacts{p: p}.removeTagRecords}
}
