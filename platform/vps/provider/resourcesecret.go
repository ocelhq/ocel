package vps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

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
	sealed, err := p.host.Kept(ctx, in.Ref.Tier, name)
	if err != nil {
		return "", err
	}
	if len(sealed) == 0 {
		minted, err := mintResourceSecret()
		if err != nil {
			return "", err
		}
		candidate, err := p.cipher.Seal(ctx, in.Ref.Tier, bound, []byte(minted))
		if err != nil {
			return "", err
		}
		if sealed, err = p.host.KeepOnce(ctx, in.Ref.Tier, name, candidate); err != nil {
			return "", err
		}
	}
	opened, err := p.cipher.Open(ctx, in.Ref.Tier, bound, sealed)
	if err != nil {
		return "", err
	}
	if len(opened) == 0 {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the credential kept for %s is empty\nRemove %s on the box",
			in.Resource.Name, host.KeptPath(in.Ref.Tier, name))
	}
	return string(opened), nil
}
