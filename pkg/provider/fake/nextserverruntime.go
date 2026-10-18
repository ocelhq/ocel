package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

const NextServerPreload = "the fake Next server preload"

func (p *Provider) ServingNextServer() *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hooks.NextServerRuntime = func(context.Context) (map[string][]byte, error) {
		return map[string][]byte{containerimage.NextServerPreloadFile: []byte(NextServerPreload)}, nil
	}
	return p
}
