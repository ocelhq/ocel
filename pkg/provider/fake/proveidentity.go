package fake

import (
	"context"

	"github.com/ocelhq/ocel/pkg/envsource"
)

func (p *Provider) ProveIdentity(_ context.Context, audience string) (envsource.IdentityProof, error) {
	return envsource.IdentityProof{IDToken: IDTokenFor(audience)}, nil
}

func IDTokenFor(audience string) string { return "fake-id-token." + audience }
