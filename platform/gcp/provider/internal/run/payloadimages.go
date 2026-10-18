package run

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sync"
)

const StaticImage = "gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2"

const binaryImageTagLen = 32

func BinaryImageTag(payload func() []byte) func() string {
	return sync.OnceValue(func() string {
		sum := sha256.New()
		sum.Write([]byte(StaticImage + "\x00"))
		sum.Write(payload())
		return hex.EncodeToString(sum.Sum(nil))[:binaryImageTagLen]
	})
}

func HostOf(uri string) string {
	if uri == "" {
		return ""
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return parsed.Host
}
