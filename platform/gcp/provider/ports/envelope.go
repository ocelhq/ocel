package ports

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	dataKeyBytes    = 32
	dataKeySeals    = 1 << 24
	dataKeyPurpose  = "data key"
	wrappedKeyBytes = 2
)

var errSealedTooShort = errors.New("the sealed value is shorter than its wrapped data key and nonce")

type Envelope struct {
	Clients *Clients
}

type dataKey struct {
	aead    cipher.AEAD
	wrapped []byte
	seals   int
}

type sealingKeys struct {
	mu   sync.Mutex
	held map[string]*dataKey
}

func dataKeyBound(bound seal.AssociatedData) seal.AssociatedData {
	if project, owned := bound.Project(); owned {
		return seal.BindToProject(project, dataKeyPurpose)
	}
	return seal.Bind(dataKeyPurpose)
}

func openedKeyID(tier environment.Tier, bound seal.AssociatedData, wrapped []byte) string {
	return string(tier) + "\x00" + string(dataKeyBound(bound).Bytes()) + "\x00" + string(wrapped)
}

func newAEAD(raw []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s Envelope) Seal(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, plaintext []byte) ([]byte, error) {
	key, err := s.sealingKey(ctx, tier, bound)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, key.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("mint a nonce: %w", err)
	}
	sealed := binary.BigEndian.AppendUint16(nil, uint16(len(key.wrapped)))
	sealed = append(append(sealed, key.wrapped...), nonce...)
	return key.aead.Seal(sealed, nonce, plaintext, bound.Bytes()), nil
}

func (s Envelope) sealingKey(ctx context.Context, tier environment.Tier, bound seal.AssociatedData) (*dataKey, error) {
	keys := &s.Clients.sealingKeys
	keys.mu.Lock()
	defer keys.mu.Unlock()
	id := string(tier) + "\x00" + string(dataKeyBound(bound).Bytes())
	if key := keys.held[id]; key != nil && key.seals < dataKeySeals {
		key.seals++
		return key, nil
	}
	raw := make([]byte, dataKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("mint a data key: %w", err)
	}
	aead, err := newAEAD(raw)
	if err != nil {
		return nil, err
	}
	wrapped, err := Cipher(s).Seal(ctx, tier, dataKeyBound(bound), raw)
	if err != nil {
		return nil, err
	}
	key := &dataKey{aead: aead, wrapped: wrapped, seals: 1}
	if keys.held == nil {
		keys.held = map[string]*dataKey{}
	}
	keys.held[id] = key
	s.Clients.openedKeys.Store(openedKeyID(tier, bound, wrapped), aead)
	return key, nil
}

func (s Envelope) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	if len(sealed) < wrappedKeyBytes {
		return nil, errSealedTooShort
	}
	length := int(binary.BigEndian.Uint16(sealed))
	if len(sealed) < wrappedKeyBytes+length {
		return nil, errSealedTooShort
	}
	wrapped, rest := sealed[wrappedKeyBytes:wrappedKeyBytes+length], sealed[wrappedKeyBytes+length:]
	aead, err := s.openingKey(ctx, tier, bound, wrapped)
	if err != nil {
		return nil, err
	}
	if len(rest) < aead.NonceSize() {
		return nil, errSealedTooShort
	}
	opened, err := aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], bound.Bytes())
	if err != nil {
		return nil, fmt.Errorf("open a value sealed under its data key: %w", err)
	}
	return opened, nil
}

func (s Envelope) openingKey(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, wrapped []byte) (cipher.AEAD, error) {
	id := openedKeyID(tier, bound, wrapped)
	if held, found := s.Clients.openedKeys.Load(id); found {
		return held.(cipher.AEAD), nil
	}
	raw, err := Cipher(s).Open(ctx, tier, dataKeyBound(bound), wrapped)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(raw)
	if err != nil {
		return nil, err
	}
	s.Clients.openedKeys.Store(id, aead)
	return aead, nil
}
