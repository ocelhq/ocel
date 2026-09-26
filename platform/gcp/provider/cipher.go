package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
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

func (s cipher) Seal(ctx context.Context, at records.SealScope, plaintext []byte) ([]byte, error) {
	opened, err := s.openCipher(ctx)
	if err != nil {
		return nil, err
	}
	return opened.Seal(ctx, at, plaintext)
}

func (s cipher) Open(ctx context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
	opened, err := s.openCipher(ctx)
	if err != nil {
		return nil, err
	}
	return opened.Open(ctx, at, sealed)
}
