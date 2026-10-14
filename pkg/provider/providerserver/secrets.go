package providerserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	generatedPasswordBytes     = 20
	generatedBypassSecretBytes = 32
	gateKeyBytes               = 32
	bypassSecretIDHexDigits    = 12
	maxBypassSecrets           = 8
)

var passwordEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func newGateRecord() (stackrecords.GateRecord, error) {
	key, err := newGateKey()
	if err != nil {
		return stackrecords.GateRecord{}, err
	}
	password, err := newPassword()
	if err != nil {
		return stackrecords.GateRecord{}, err
	}
	return stackrecords.GateRecord{
		Gate: gate.Gate{
			Key:           key,
			PasswordMAC:   gate.HashPassword(key, password),
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

func newPassword() (string, error) {
	return newRandomText(generatedPasswordBytes, func(raw []byte) string {
		return strings.ToLower(passwordEncoding.EncodeToString(raw))
	}, "a password")
}

func newBypassSecret() (string, error) {
	return newRandomText(generatedBypassSecretBytes, hex.EncodeToString, "a bypass secret")
}

func newGateKey() (string, error) {
	return newRandomText(gateKeyBytes, hex.EncodeToString, "the key that signs sessions")
}

func newRandomText(bytes int, encode func([]byte) string, what string) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate %s: %w", what, err)
	}
	return encode(raw), nil
}
