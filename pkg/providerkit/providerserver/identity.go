package providerserver

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func newPromotionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint a promotion id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
