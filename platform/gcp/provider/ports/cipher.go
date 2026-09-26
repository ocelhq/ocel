package ports

import (
	"context"
	"fmt"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Cipher struct {
	Clients *Clients
}

func (s Cipher) key(at records.SealScope) (string, error) {
	if at.Class == "" {
		return "", Classless("a value")
	}
	return s.Clients.KeyPath(string(at.Class)), nil
}

func (s Cipher) Seal(ctx context.Context, at records.SealScope, plaintext []byte) ([]byte, error) {
	key, err := s.key(at)
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
		AdditionalAuthenticatedData: at.AAD(),
	})
	if err != nil {
		return nil, s.keyless(at.Class, "encrypt value", err)
	}
	return sealed.GetCiphertext(), nil
}

func (s Cipher) Open(ctx context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
	key, err := s.key(at)
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
		AdditionalAuthenticatedData: at.AAD(),
	})
	if err != nil {
		return nil, s.keyless(at.Class, "decrypt value", err)
	}
	return opened.GetPlaintext(), nil
}

func (s Cipher) keyless(class edge.Class, doing string, err error) error {
	if status.Code(err) == codes.NotFound {
		return refusal.Refuse(refusal.CodeNotReady,
			"this project holds no %s key on the %s ring to seal a %s value under, and a key is the one bootstrap item with a standing cost.\nRun `%s` to add one, then try again",
			class, s.Clients.KeyRing(), class, providerkit.BootstrapVarsKeyCommand(class))
	}
	return fmt.Errorf("%s: %w", doing, err)
}
