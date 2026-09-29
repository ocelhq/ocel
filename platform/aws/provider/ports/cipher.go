package ports

import (
	"context"
	"fmt"
	"maps"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

type CryptoAPI interface {
	Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

type Cipher struct {
	KMS  CryptoAPI
	Keys Keys
}

type Keys interface {
	Key(ctx context.Context, tier environment.Tier) (string, error)
}

type Key string

func (k Key) Key(context.Context, environment.Tier) (string, error) { return string(k), nil }

func (s Cipher) readKey(ctx context.Context, tier environment.Tier) (string, error) {
	if tier == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"a value names no tier, and this account seals each tier's values under the key its own bootstrap made")
	}
	if s.Keys == nil {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"nothing in this account has a key to seal a %s value under.\nRun `%s`, then try again",
			tier, provider.BootstrapVariablesKeyCommand(tier))
	}
	key, err := s.Keys.Key(ctx, tier)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the %s bootstrap has no key to seal a value under, and a key is the one bootstrap item with a recurring cost.\nRun `%s` to add one, then try again",
			tier, provider.BootstrapVariablesKeyCommand(tier))
	}
	return key, nil
}

func (s Cipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	encryptionContext, err := newEncryptionContext(bound)
	if err != nil {
		return nil, err
	}
	key, err := s.readKey(ctx, tier)
	if err != nil {
		return nil, err
	}
	out, err := s.KMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(key),
		Plaintext:         plaintext,
		EncryptionContext: encryptionContext,
	})
	if err != nil {
		return nil, fmt.Errorf("encrypt value: %w", err)
	}
	return out.CiphertextBlob, nil
}

func (s Cipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	encryptionContext, err := newEncryptionContext(bound)
	if err != nil {
		return nil, err
	}
	key, err := s.readKey(ctx, tier)
	if err != nil {
		return nil, err
	}
	out, err := s.KMS.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             aws.String(key),
		CiphertextBlob:    sealed,
		EncryptionContext: encryptionContext,
	})
	if err != nil {
		return nil, fmt.Errorf("decrypt value: %w", err)
	}
	return out.Plaintext, nil
}

func newEncryptionContext(bound seal.AssociatedData) (map[string]string, error) {
	encryptionContext := make(map[string]string, len(bound))
	for _, field := range bound {
		if field.Name == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid, "a sealed value is bound to a field with no name")
		}
		if _, named := encryptionContext[field.Name]; named {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"a sealed value is bound to %s twice, and an encryption context holds one value per name", field.Name)
		}
		encryptionContext[field.Name] = field.Value
	}
	maps.DeleteFunc(encryptionContext, func(_, value string) bool { return value == "" })
	return encryptionContext, nil
}
