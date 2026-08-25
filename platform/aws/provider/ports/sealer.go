package ports

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
)

type CryptoAPI interface {
	Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

type Sealer struct {
	KMS  CryptoAPI
	Keys Keys
}

type Keys interface {
	Key(ctx context.Context, class kit.Class) (string, error)
}

type Key string

func (k Key) Key(context.Context, kit.Class) (string, error) { return string(k), nil }

func (s Sealer) key(ctx context.Context, at kit.Coordinate) (string, error) {
	if at.Class == "" {
		return "", kit.Refuse(kit.CodeInvalid,
			"a value names no class, and this account seals each class's values under the key its own bootstrap made")
	}
	if s.Keys == nil {
		return "", kit.Refuse(kit.CodeNotReady,
			"this account has no Ocel bootstrap, so there is no key to seal a value under.\nRun `%s` to create it, then try again", providerkit.BootstrapCommand(at.Class))
	}
	return s.Keys.Key(ctx, at.Class)
}

func (s Sealer) Seal(ctx context.Context, at kit.Coordinate, plaintext []byte) ([]byte, error) {
	bound, err := encryptionContext(at)
	if err != nil {
		return nil, err
	}
	key, err := s.key(ctx, at)
	if err != nil {
		return nil, err
	}
	out, err := s.KMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(key),
		Plaintext:         plaintext,
		EncryptionContext: bound,
	})
	if err != nil {
		return nil, fmt.Errorf("encrypt value: %w", err)
	}
	return out.CiphertextBlob, nil
}

func (s Sealer) Open(ctx context.Context, at kit.Coordinate, sealed []byte) ([]byte, error) {
	bound, err := encryptionContext(at)
	if err != nil {
		return nil, err
	}
	key, err := s.key(ctx, at)
	if err != nil {
		return nil, err
	}
	out, err := s.KMS.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             aws.String(key),
		CiphertextBlob:    sealed,
		EncryptionContext: bound,
	})
	if err != nil {
		return nil, fmt.Errorf("decrypt value: %w", err)
	}
	return out.Plaintext, nil
}

func encryptionContext(at kit.Coordinate) (map[string]string, error) {
	bound := map[string]string{
		"project":     at.Project,
		"class":       string(at.Class),
		"environment": at.Env,
		"folder":      at.Folder,
		"key":         at.Name,
	}
	for name, value := range bound {
		if value == "" {
			return nil, kit.Refuse(kit.CodeInvalid, "a value's coordinate names no %s, and the coordinate is what a sealed value is bound to", name)
		}
	}
	if at.Link != "" {
		bound["link"] = at.Link
	}
	return bound, nil
}
