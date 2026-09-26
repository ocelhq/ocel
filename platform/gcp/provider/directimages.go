package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
)

func (p *Provider) OpenDirectImages(context.Context) (images.ImageStore, error) {
	return images.DaemonImages(), nil
}
