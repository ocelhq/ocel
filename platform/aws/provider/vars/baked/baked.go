package baked

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
)

const (
	FilePath = ".ocel/variables.enc"

	EnvelopeVar = "OCEL_VARS_ENVELOPE"

	Prefix = "OCEL_VAR_"

	KeyBytes   = 32
	NonceBytes = 12
)

func Seal(key []byte, values map[string]string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode baked variables: %w", err)
	}
	nonce := make([]byte, NonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate baked variable nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, payload, nil), nil
}

func Open(key, sealed []byte) (map[string]string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < NonceBytes {
		return nil, fmt.Errorf("baked variables are %d bytes, too short to hold a nonce", len(sealed))
	}
	payload, err := gcm.Open(nil, sealed[:NonceBytes], sealed[NonceBytes:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt baked variables: %w", err)
	}
	var values map[string]string
	if err := json.Unmarshal(payload, &values); err != nil {
		return nil, fmt.Errorf("decode baked variables: %w", err)
	}
	return values, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("baked variable data key is %d bytes, want %d", len(key), KeyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
