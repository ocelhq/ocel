package gcp

import "github.com/ocelhq/ocel/platform/gcp/provider/assets"

type artifacts struct {
	p *Provider
}

func (p *Provider) objects() *assets.Store {
	return &assets.Store{OpenClients: p.openClients, RemoveTagRecords: artifacts{p: p}.removeTagRecords}
}
