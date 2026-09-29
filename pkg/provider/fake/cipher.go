package fake

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"maps"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

type Cipher struct {
	mu      sync.Mutex
	keys    map[environment.Tier][]byte
	refusal error
}

func NewCipher() *Cipher {
	return &Cipher{keys: map[environment.Tier][]byte{}}
}

func NewCipherWithKeys(keys map[environment.Tier][]byte) *Cipher {
	return &Cipher{keys: maps.Clone(keys)}
}

func (s *Cipher) Seal(_ context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	gcm, err := s.gcm(tier)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, bound.Bytes()), nil
}

func (s *Cipher) RefuseOpening(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusal = err
}

func (s *Cipher) Open(_ context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	s.mu.Lock()
	refused := s.refusal
	s.mu.Unlock()
	if refused != nil {
		return nil, refused
	}
	gcm, err := s.gcm(tier)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, refusal.Refuse(refusal.CodeInvalid, "sealed value is truncated")
	}
	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, bound.Bytes())
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeDenied, "sealed value does not open under the %s key bound to %s", tier, bound.Bytes())
	}
	return plaintext, nil
}

func (s *Cipher) gcm(tier environment.Tier) (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.ensureKey(tier))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Cipher) ensureKey(tier environment.Tier) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key, ok := s.keys[tier]; ok {
		return key
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("fake: mint sealing key: " + err.Error())
	}
	s.keys[tier] = key
	return key
}
