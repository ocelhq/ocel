package cloudflare

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const envelopeKeyBinding = "OCEL_ENVELOPE_KEY"

const envelopeKeyBytes = 32

func mintEnvelopeKey() (string, error) {
	key := make([]byte, envelopeKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("mint the envelope key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func envelopeCipher(key string) (cipher.AEAD, error) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("decode the envelope key: %w", err)
	}
	if len(raw) != envelopeKeyBytes {
		return nil, fmt.Errorf("the envelope key is %d bytes, want %d", len(raw), envelopeKeyBytes)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func wrapEnvelope(key, envelope string) (string, error) {
	if envelope == "" {
		return "", nil
	}
	dataKey, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		return "", fmt.Errorf("decode the envelope: %w", err)
	}
	aead, err := envelopeCipher(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("mint the envelope nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, dataKey, nil)), nil
}

func unwrapEnvelope(key, wrapped string) (string, error) {
	framed, err := base64.StdEncoding.DecodeString(wrapped)
	if err != nil {
		return "", fmt.Errorf("decode the wrapped envelope: %w", err)
	}
	aead, err := envelopeCipher(key)
	if err != nil {
		return "", err
	}
	if len(framed) < aead.NonceSize() {
		return "", errors.New("the wrapped envelope is shorter than its nonce")
	}
	dataKey, err := aead.Open(nil, framed[:aead.NonceSize()], framed[aead.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("unwrap the envelope: %w", err)
	}
	return base64.StdEncoding.EncodeToString(dataKey), nil
}
