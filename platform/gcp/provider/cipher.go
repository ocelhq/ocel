package gcp

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

type cipher struct{ p *Provider }

func (s cipher) openKMS(ctx context.Context, tier environment.Tier) (*gcloud.Clients, ports.Cipher, error) {
	if tier == "" {
		return nil, ports.Cipher{}, ports.Tierless("a value")
	}
	opened, err := s.p.openClients(ctx)
	if err != nil {
		return nil, ports.Cipher{}, err
	}
	return opened, ports.Cipher{Clients: opened.Workload()}, nil
}

func (s cipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	c, opened, err := s.openKMS(ctx, tier)
	if err != nil {
		return nil, err
	}
	sealed, err := opened.Seal(ctx, tier, bound, plaintext)
	if !errors.Is(err, ports.ErrNoProjectKey) && !errors.Is(err, ports.ErrNoTierKey) {
		return sealed, err
	}
	project, owned := ports.ProjectSealedFor(bound)
	if err := s.p.ensureKeys(ctx, tier, project); err != nil {
		return nil, err
	}
	if owned {
		if err := variables.EnsureProjectKey(ctx, c, tier, project); err != nil {
			return nil, err
		}
	}
	err = googleretry.UntilServed(ctx, func() error {
		sealed, err = opened.Seal(ctx, tier, bound, plaintext)
		return err
	})
	return sealed, err
}

func (s cipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	_, opened, err := s.openKMS(ctx, tier)
	if err != nil {
		return nil, err
	}
	return opened.Open(ctx, tier, bound, sealed)
}

func (p *Provider) ensureKeys(ctx context.Context, tier environment.Tier, addedBy string) error {
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.EnsurePart(ctx, tier, addedBy, bootstrap.Keys.For(b, tier))
}
