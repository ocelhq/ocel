package ports

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

const associatedDataContextKey = "ocel:associated-data"

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
		return "", provider.WantedPartError{Part: provider.VariablesKeyPart, Err: refusal.Refuse(refusal.CodeNotReady,
			"nothing in this account has a key to seal a %s value under.\nRun `%s`, then try again",
			tier, provider.BootstrapCommand(tier))}
	}
	key, err := s.Keys.Key(ctx, tier)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", RefuseUnkeyed(tier)
	}
	return key, nil
}

func RefuseUnkeyed(tier environment.Tier) error {
	return provider.WantedPartError{Part: provider.VariablesKeyPart, Err: refusal.Refuse(refusal.CodeNotReady,
		"the %s bootstrap has no key to seal a value under, and a key is the one bootstrap item with a recurring cost.\nRun `%s` to add one, then try again",
		tier, provider.BootstrapCommand(tier))}
}

func (s Cipher) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	key, err := s.readKey(ctx, tier)
	if err != nil {
		return nil, err
	}
	out, err := s.KMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(key),
		Plaintext:         plaintext,
		EncryptionContext: newEncryptionContext(bound),
	})
	if err != nil {
		return nil, fmt.Errorf("encrypt value: %w", err)
	}
	return out.CiphertextBlob, nil
}

func (s Cipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	key, err := s.readKey(ctx, tier)
	if err != nil {
		return nil, err
	}
	out, err := s.KMS.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             aws.String(key),
		CiphertextBlob:    sealed,
		EncryptionContext: newEncryptionContext(bound),
	})
	if err != nil {
		return nil, fmt.Errorf("decrypt value: %w", err)
	}
	return out.Plaintext, nil
}

func newEncryptionContext(bound seal.AssociatedData) map[string]string {
	return map[string]string{associatedDataContextKey: base64.StdEncoding.EncodeToString(bound.Bytes())}
}
