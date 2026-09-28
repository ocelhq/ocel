package ports

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
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

func (s Cipher) key(ctx context.Context, at records.SealScope) (string, error) {
	if at.Tier == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"a value names no tier, and this account seals each tier's values under the key its own bootstrap made")
	}
	if s.Keys == nil {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"nothing in this account has a key to seal a %s value under.\nRun `%s`, then try again",
			at.Tier, provider.BootstrapVarsKeyCommand(at.Tier))
	}
	key, err := s.Keys.Key(ctx, at.Tier)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"the %s bootstrap has no key to seal a value under, and a key is the one bootstrap item with a recurring cost.\nRun `%s` to add one, then try again",
			at.Tier, provider.BootstrapVarsKeyCommand(at.Tier))
	}
	return key, nil
}

func (s Cipher) Seal(ctx context.Context, at records.SealScope, plaintext []byte) ([]byte, error) {
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

func (s Cipher) Open(ctx context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
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

func encryptionContext(at records.SealScope) (map[string]string, error) {
	bound := map[string]string{
		"project":     at.Project,
		"class":       string(at.Tier),
		"environment": at.Env,
		"folder":      at.Folder,
		"key":         at.Name,
	}
	for name, value := range bound {
		if value == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid, "a value's coordinate names no %s, and the coordinate is what a sealed value is bound to", name)
		}
	}
	if at.Binding != "" {
		bound["binding"] = at.Binding
	}
	return bound, nil
}
