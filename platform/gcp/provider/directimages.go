package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func (p *Provider) OpenDirectImages(context.Context) (provider.ImageStore, error) {
	return images.DaemonStore(), nil
}
