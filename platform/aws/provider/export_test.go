package aws

import "github.com/ocelhq/ocel/pkg/images"

func (p *Provider) PullingImagesWith(pull images.ImagePull) *Provider {
	p.pullImage = pull
	return p
}
