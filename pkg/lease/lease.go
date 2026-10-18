package lease

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const tokenBytes = 16

func NewToken() (string, error) {
	token := make([]byte, tokenBytes)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("mint an environment lease token: %w", err)
	}
	return hex.EncodeToString(token), nil
}
