package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) DirectImages(context.Context) (providerkit.ImageStore, error) {
	return providerkit.DaemonImages(), nil
}
