// Package baked is the encrypted-baked delivery format: the single definition
// of how a sensitive variable is sealed into a deployment bundle and opened
// again by the membrane that starts the application process.
//
// It deliberately carries no cloud dependency. The deploy draws a data key,
// seals under it and ships the key in the function's configuration; the
// membrane reads that key back and opens the bundle. Both sides are local, so
// this package is shared by the provider and by the runtime bootstrap without
// the latter linking anything it does not call.
package baked

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
)

const (
	// FilePath is where an app's sealed values ride inside every one of its
	// function deployment packages, relative to the task root.
	FilePath = ".ocel/variables.enc"

	// EnvelopeVar names the one function-configuration entry an encrypted-baked
	// value contributes: the base64 data key this deployment's bundle was
	// sealed under. It is drawn per deploy, so it opens that bundle and no
	// other, and the values themselves stay out of the configuration entirely.
	EnvelopeVar = "OCEL_VARS_ENVELOPE"

	// Prefix is the namespaced name the membrane injects an opened value under.
	// It sits inside the SDK's reserved prefixes, so nothing a user may declare
	// can shadow a value that was meant to be encrypted.
	Prefix = "OCEL_VAR_"

	// KeyBytes pins AES-256; NonceBytes is GCM's standard nonce length.
	KeyBytes   = 32
	NonceBytes = 12
)

// Seal encrypts values under key, returning the bytes that ride in the bundle:
// a fresh random nonce followed by the AEAD ciphertext. Every call produces
// different bytes, so a rotation always lands as a new artifact and no
// (key, nonce) pair is ever reused.
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

// Open reverses Seal. Every failure — a key from another substrate, a
// truncated file, a byte altered in transit — is an error rather than an empty
// result, because a variable silently unset is indistinguishable at the point
// of use from one that was never required.
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
