package edge

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
)

const PreviewKeyVar = "OCEL_PREVIEW_KEY"

const (
	PreviewTokenLen = 16

	previewMACLen = 8

	PreviewTailLen = PreviewTokenLen + previewMACLen
)

var previewEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

type PreviewKey string

func NewPreviewKey() (PreviewKey, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint the key that signs preview hostnames: %w", err)
	}
	return PreviewKey(hex.EncodeToString(raw[:])), nil
}

func NewPreviewToken() (string, error) {
	var raw [PreviewTokenLen * 5 / 8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint a preview hostname token: %w", err)
	}
	return previewEncoding.EncodeToString(raw[:]), nil
}

func (k PreviewKey) Sign(prefix, token string) string {
	signed := prefix + "-" + token
	return signed + k.computeMAC(signed)
}

func (k PreviewKey) computeMAC(signed string) string {
	sum := hmac.New(sha256.New, []byte(k))
	sum.Write([]byte(signed))
	return previewEncoding.EncodeToString(sum.Sum(nil))[:previewMACLen]
}
