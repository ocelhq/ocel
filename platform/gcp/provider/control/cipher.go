package control

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type cipher struct{ p *Provider }

func (s cipher) openKMS(ctx context.Context, tier environment.Tier) (ports.Cipher, error) {
	if tier == "" {
		return ports.Cipher{}, ports.Tierless("a value")
	}
	opened, err := s.p.OpenClients(ctx)
	if err != nil {
		return ports.Cipher{}, err
	}
	return ports.Cipher{Clients: opened.Workload()}, nil
}

func sealingProject(bound seal.AssociatedData) string {
	project, owned := bound.Project()
	if !owned || naming.Validate("project", project) != nil {
		return ""
	}
	return project
}

func (s cipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	opened, err := s.openKMS(ctx, tier)
	if err != nil {
		return nil, err
	}
	sealed, err := opened.Seal(ctx, tier, bound, plaintext)
	if !errors.Is(err, ports.ErrNoTierKey) {
		return sealed, err
	}
	if err := s.p.Engine.EnsureTierKeys(ctx, tier, sealingProject(bound)); err != nil {
		return nil, err
	}
	err = googleretry.UntilServed(ctx, func() error {
		sealed, err = opened.Seal(ctx, tier, bound, plaintext)
		return err
	})
	return sealed, err
}

func (s cipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	opened, err := s.openKMS(ctx, tier)
	if err != nil {
		return nil, err
	}
	return opened.Open(ctx, tier, bound, sealed)
}
