package fake

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Sealer struct {
	key []byte
}

func NewSealer() *Sealer {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("fake: mint sealing key: " + err.Error())
	}
	return &Sealer{key: key}
}

func (s *Sealer) Seal(_ context.Context, at providerkit.Coordinate, plaintext []byte) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, additionalData(at)), nil
}

func (s *Sealer) Open(_ context.Context, at providerkit.Coordinate, sealed []byte) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "sealed value is truncated")
	}
	nonce, body := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, additionalData(at))
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied, "sealed value does not open at %s", additionalData(at))
	}
	return plaintext, nil
}

func (s *Sealer) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func additionalData(at providerkit.Coordinate) []byte {
	var b bytes.Buffer
	for _, part := range []string{at.Project, string(at.Class), at.Env, at.Folder, at.Link, at.Name} {
		b.WriteString(strings.ReplaceAll(part, "/", "%2F"))
		b.WriteByte('/')
	}
	return b.Bytes()
}
