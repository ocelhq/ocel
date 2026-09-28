package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type cipher struct{ p *Provider }

func (s cipher) openCipher(ctx context.Context) (ports.Cipher, error) {
	opened, err := s.p.openClients(ctx)
	if err != nil {
		return ports.Cipher{}, err
	}
	return ports.Cipher{Clients: opened.Workload()}, nil
}

func (s cipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	opened, err := s.openCipher(ctx)
	if err != nil {
		return nil, err
	}
	return opened.Seal(ctx, tier, bound, plaintext)
}

func (s cipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	opened, err := s.openCipher(ctx)
	if err != nil {
		return nil, err
	}
	return opened.Open(ctx, tier, bound, sealed)
}
