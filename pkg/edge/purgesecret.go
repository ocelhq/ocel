package edge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func DerivePurgeSecret(seed, isrPrefix string) string {
	mac := hmac.New(sha256.New, []byte(seed))
	mac.Write([]byte(isrPrefix))
	return hex.EncodeToString(mac.Sum(nil))
}
