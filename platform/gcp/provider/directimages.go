package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func (p *Provider) OpenDirectImages(context.Context) (providerkit.ImageStore, error) {
	return providerkit.DaemonImages(), nil
}
