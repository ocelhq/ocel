package ports

import (
	"context"
	"fmt"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

type Cipher struct {
	Clients *Clients
}

func (s Cipher) resolveKeyPath(tier environment.Tier) (string, error) {
	if tier == "" {
		return "", Tierless("a value")
	}
	return s.Clients.KeyPath(string(tier)), nil
}

func (s Cipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	key, err := s.resolveKeyPath(tier)
	if err != nil {
		return nil, err
	}
	client, err := s.Clients.KMS()
	if err != nil {
		return nil, err
	}
	sealed, err := client.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:                        key,
		Plaintext:                   plaintext,
		AdditionalAuthenticatedData: bound.Bytes(),
	})
	if err != nil {
		return nil, s.keyless(tier, "encrypt value", err)
	}
	return sealed.GetCiphertext(), nil
}

func (s Cipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	key, err := s.resolveKeyPath(tier)
	if err != nil {
		return nil, err
	}
	client, err := s.Clients.KMS()
	if err != nil {
		return nil, err
	}
	opened, err := client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:                        key,
		Ciphertext:                  sealed,
		AdditionalAuthenticatedData: bound.Bytes(),
	})
	if err != nil {
		return nil, s.keyless(tier, "decrypt value", err)
	}
	return opened.GetPlaintext(), nil
}

func (s Cipher) keyless(tier environment.Tier, doing string, err error) error {
	if status.Code(err) == codes.NotFound {
		return refusal.Refuse(refusal.CodeNotReady,
			"this project has no %s key on the %s ring to seal a %s value under, and a key is the one bootstrap item with a recurring cost.\nRun `%s` to add one, then try again",
			tier, s.Clients.KeyRing(), tier, provider.BootstrapVarsKeyCommand(tier))
	}
	return fmt.Errorf("%s: %w", doing, err)
}
