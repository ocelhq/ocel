package component

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
)

const (
	secretLen    = 24
	SecretFolder = "resources"
)

func MintSecret() (string, error) {
	raw := make([]byte, secretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint a password: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (b Box) KeptSecret(ctx context.Context, in resources.ProvisionRequest, name string, bound seal.AssociatedData) (string, error) {
	_, opened, err := b.KeptSealed(ctx, in.Ref.Tier, name, in.Resource.Name, bound, MintSecret)
	return opened, err
}

func (b Box) KeptSealed(ctx context.Context, tier environment.Tier, name, resource string, bound seal.AssociatedData, mint func() (string, error)) ([]byte, string, error) {
	sealed, err := b.Containers.Kept(ctx, tier, name)
	if err != nil {
		return nil, "", err
	}
	if len(sealed) == 0 {
		minted, err := mint()
		if err != nil {
			return nil, "", err
		}
		candidate, err := b.Cipher.Seal(ctx, tier, bound, []byte(minted))
		if err != nil {
			return nil, "", err
		}
		if sealed, err = b.Containers.KeepOnce(ctx, tier, name, candidate); err != nil {
			return nil, "", err
		}
	}
	opened, err := b.OpenKept(ctx, tier, name, resource, bound, sealed)
	return sealed, opened, err
}

func (b Box) ReadKeptSealed(ctx context.Context, tier environment.Tier, name, resource string, bound seal.AssociatedData) ([]byte, string, error) {
	sealed, err := b.Containers.Kept(ctx, tier, name)
	if err != nil || len(sealed) == 0 {
		return nil, "", err
	}
	opened, err := b.OpenKept(ctx, tier, name, resource, bound, sealed)
	return sealed, opened, err
}

func (b Box) OpenKept(ctx context.Context, tier environment.Tier, name, resource string, bound seal.AssociatedData, sealed []byte) (string, error) {
	opened, err := b.Cipher.Open(ctx, tier, bound, sealed)
	if err != nil {
		return "", err
	}
	if len(opened) == 0 {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the credential kept for %s is empty\nRemove %s on the box",
			resource, container.KeptPath(tier, name))
	}
	return string(opened), nil
}
