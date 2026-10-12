package providerserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	generatedPasswordBytes     = 20
	generatedBypassSecretBytes = 32
	previewGateKeyBytes        = 32
	bypassSecretIDHexDigits    = 12
	maxBypassSecrets           = 8
)

var passwordEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func newPreviewGateRecord() (stackrecords.PreviewGateRecord, error) {
	key, err := newPreviewGateKey()
	if err != nil {
		return stackrecords.PreviewGateRecord{}, err
	}
	password, err := newPreviewPassword()
	if err != nil {
		return stackrecords.PreviewGateRecord{}, err
	}
	return stackrecords.PreviewGateRecord{
		Gate: previewgate.Gate{
			Key:           key,
			PasswordMAC:   previewgate.HashPassword(key, password),
			BypassSecrets: []string{},
			AllowOptions:  []string{},
		},
		Password: password,
		Enabled:  true,
		Pending:  true,
	}, nil
}

func deriveBypassSecretID(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])[:bypassSecretIDHexDigits]
}

func newPreviewPassword() (string, error) {
	return newRandomText(generatedPasswordBytes, func(raw []byte) string {
		return strings.ToLower(passwordEncoding.EncodeToString(raw))
	}, "a preview password")
}

func newBypassSecret() (string, error) {
	return newRandomText(generatedBypassSecretBytes, hex.EncodeToString, "a preview bypass secret")
}

func newPreviewGateKey() (string, error) {
	return newRandomText(previewGateKeyBytes, hex.EncodeToString, "the key that signs preview sessions")
}

func newRandomText(bytes int, encode func([]byte) string, what string) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate %s: %w", what, err)
	}
	return encode(raw), nil
}
