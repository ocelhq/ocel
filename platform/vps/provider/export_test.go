package vps

import (
	"net/http"

	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var ProviderOver = newProvider

func (p *Provider) Host() *host.Host { return p.host }

func (p *Provider) Probing(client *http.Client) { p.probing = client }
