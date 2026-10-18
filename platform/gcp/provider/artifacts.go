package gcp

import "github.com/ocelhq/ocel/platform/gcp/provider/assets"

type artifacts struct {
	p *Provider
}

func (p *Provider) objects() *assets.Store {
	return &assets.Store{OpenClients: p.openClients, RemoveTagRecords: artifacts{p: p}.removeTagRecords}
}

// TODO: cachegrant.go calls assets.CacheAppPrefix itself once the tag records scoping change to it lands; drop this then.
func cacheAppPrefix(key string) (string, error) { return assets.CacheAppPrefix(key) }
