package machine

import (
	_ "embed"
)

const SealAlgorithm = "aes-256-gcm"

type Seal struct {
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
	CreatedAt   string `json:"createdAt"`
}
