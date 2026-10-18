package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

const NextServerPreload = "the fake Next server preload"

func (p *Provider) ServingNextServer() *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.servesNextServer = true
	return p
}

func (r containerRuntime) NextServer(context.Context) (map[string][]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.servesNextServer {
		return nil, nil
	}
	return map[string][]byte{containerimage.NextServerPreloadFile: []byte(NextServerPreload)}, nil
}
