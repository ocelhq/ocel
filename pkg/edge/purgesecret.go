package edge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func DerivePurgeSecret(seed, storageCoordinate string) string {
	mac := hmac.New(sha256.New, []byte(seed))
	mac.Write([]byte(storageCoordinate))
	return hex.EncodeToString(mac.Sum(nil))
}
