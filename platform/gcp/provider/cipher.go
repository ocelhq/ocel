package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type cipher struct{ p *Provider }

func (s cipher) stood(ctx context.Context) (ports.Cipher, error) {
	held, err := s.p.stood(ctx)
	if err != nil {
		return ports.Cipher{}, err
	}
	return ports.Cipher{Clients: held.Workload()}, nil
}

func (s cipher) Seal(ctx context.Context, at records.SealScope, plaintext []byte) ([]byte, error) {
	held, err := s.stood(ctx)
	if err != nil {
		return nil, err
	}
	return held.Seal(ctx, at, plaintext)
}

func (s cipher) Open(ctx context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
	held, err := s.stood(ctx)
	if err != nil {
		return nil, err
	}
	return held.Open(ctx, at, sealed)
}
