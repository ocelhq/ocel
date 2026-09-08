package gcp

import (
	"context"
	"fmt"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const KeyRing = "ocel"

type sealer struct {
	clients *clients
}

func (s sealer) key(at providerkit.Coordinate) (string, error) {
	if at.Class == "" {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"a value names no class, and this project seals each class's values under a key of its own")
	}
	return fmt.Sprintf("projects/%s/locations/%s/keyRings/%s/cryptoKeys/%s",
		s.clients.project, s.clients.region, KeyRing, at.Class), nil
}

func (s sealer) Seal(ctx context.Context, at providerkit.Coordinate, plaintext []byte) ([]byte, error) {
	key, err := s.key(at)
	if err != nil {
		return nil, err
	}
	client, err := s.clients.KMS()
	if err != nil {
		return nil, err
	}
	sealed, err := client.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:                        key,
		Plaintext:                   plaintext,
		AdditionalAuthenticatedData: at.Binding(),
	})
	if err != nil {
		return nil, keyless(at.Class, "encrypt value", err)
	}
	return sealed.GetCiphertext(), nil
}

func (s sealer) Open(ctx context.Context, at providerkit.Coordinate, sealed []byte) ([]byte, error) {
	key, err := s.key(at)
	if err != nil {
		return nil, err
	}
	client, err := s.clients.KMS()
	if err != nil {
		return nil, err
	}
	opened, err := client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:                        key,
		Ciphertext:                  sealed,
		AdditionalAuthenticatedData: at.Binding(),
	})
	if err != nil {
		return nil, keyless(at.Class, "decrypt value", err)
	}
	return opened.GetPlaintext(), nil
}

func keyless(class providerkit.Class, doing string, err error) error {
	if status.Code(err) == codes.NotFound {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"this project holds no key to seal a %s value under, and a key is the one bootstrap item with a standing cost.\nRun `%s` to add one, then try again",
			class, providerkit.BootstrapVarsKeyCommand(class))
	}
	return fmt.Errorf("%s: %w", doing, err)
}
