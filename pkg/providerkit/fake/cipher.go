package fake

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

type Cipher struct {
	key []byte
}

func NewCipher() *Cipher {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("fake: mint sealing key: " + err.Error())
	}
	return &Cipher{key: key}
}

func (s *Cipher) Seal(_ context.Context, at records.SealScope, plaintext []byte) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, at.AAD()), nil
}

func (s *Cipher) Open(_ context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, refusal.Refuse(refusal.CodeInvalid, "sealed value is truncated")
	}
	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, at.AAD())
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeDenied, "sealed value does not open at %s", at.AAD())
	}
	return plaintext, nil
}

func (s *Cipher) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
