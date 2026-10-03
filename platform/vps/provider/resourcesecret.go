package vps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	resourceSecretLen    = 24
	resourceSecretFolder = "resources"
)

func mintResourceSecret() (string, error) {
	raw := make([]byte, resourceSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint a password: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (p *Provider) keptSecret(ctx context.Context, in resources.ProvisionRequest, name string, bound seal.AssociatedData) (string, error) {
	_, opened, err := p.keptSealed(ctx, in.Ref.Tier, name, in.Resource.Name, bound, mintResourceSecret)
	return opened, err
}

func (p *Provider) keptSealed(ctx context.Context, tier environment.Tier, name, resource string, bound seal.AssociatedData, mint func() (string, error)) ([]byte, string, error) {
	sealed, err := p.host.Kept(ctx, tier, name)
	if err != nil {
		return nil, "", err
	}
	if len(sealed) == 0 {
		minted, err := mint()
		if err != nil {
			return nil, "", err
		}
		candidate, err := p.cipher.Seal(ctx, tier, bound, []byte(minted))
		if err != nil {
			return nil, "", err
		}
		if sealed, err = p.host.KeepOnce(ctx, tier, name, candidate); err != nil {
			return nil, "", err
		}
	}
	opened, err := p.cipher.Open(ctx, tier, bound, sealed)
	if err != nil {
		return nil, "", err
	}
	if len(opened) == 0 {
		return nil, "", refusal.Refuse(refusal.CodeNotReady,
			"the credential kept for %s is empty\nRemove %s on the box",
			resource, host.KeptPath(tier, name))
	}
	return sealed, string(opened), nil
}
