// Package runtime is the production implementation of runtime.v1.RuntimeService:
// it mints S3 presigned PUT targets, persists a pending upload session (with a
// per-session HMAC secret) to DynamoDB, verifies completion signatures, and
// reports session status. It is the permanent Go implementation of the cloud
// mechanics behind ocel/blob uploads; dev delegates the same contract to the
// Ocel API instead.
package runtime

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SignedFile is the file identity a completion callback signs over. It mirrors
// the proto CompletedFile; size is a plain JSON number (single-PUT uploads sit
// well within a safe integer).
type SignedFile struct {
	Key      string
	Name     string
	Size     int64
	MimeType string
}

// canonicalFile fixes the field order the HMAC covers. Never reorder: the
// detector (which signs) and VerifyUploadSignature (which re-derives) must
// serialize byte-identically. This mirrors the dev signer's
// canonicalUploadPayload (packages/api/src/routes/blob/signing.ts) so the two
// implementations agree on the canonical bytes.
type canonicalFile struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
}

type canonicalPayload struct {
	SessionID string        `json:"sessionId"`
	File      canonicalFile `json:"file"`
}

// CanonicalUploadPayload is the exact byte sequence the per-session HMAC covers.
// It matches JSON.stringify({sessionId, file:{key,name,size,mimeType}}) with no
// whitespace and no HTML escaping, so it is byte-identical to the TS scheme.
func CanonicalUploadPayload(sessionID string, file SignedFile) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// JSON.stringify does not escape <, >, or &; disable Go's HTML escaping so
	// the canonical bytes match exactly for keys/names containing those runes.
	enc.SetEscapeHTML(false)
	// Encode cannot fail for these plain string/int fields; the error is
	// unreachable and deliberately dropped.
	_ = enc.Encode(canonicalPayload{
		SessionID: sessionID,
		File: canonicalFile{
			Key:      file.Key,
			Name:     file.Name,
			Size:     file.Size,
			MimeType: file.MimeType,
		},
	})
	// Encoder.Encode appends a trailing newline; JSON.stringify does not.
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// signUpload is HMAC-SHA256 over the canonical payload, hex-encoded. This is the
// signature the detector carries on op=callback; the secret stays in the store.
func signUpload(secret, sessionID string, file SignedFile) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(CanonicalUploadPayload(sessionID, file))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyUpload constant-time compares a presented signature against the
// re-derived one, returning false on any length mismatch.
func verifyUpload(secret, sessionID string, file SignedFile, signature string) bool {
	expected := signUpload(secret, sessionID, file)
	return hmac.Equal([]byte(expected), []byte(signature))
}
