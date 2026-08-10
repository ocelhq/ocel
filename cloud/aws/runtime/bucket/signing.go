package bucket

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type SignedFile struct {
	Key      string
	Name     string
	Size     int64
	MimeType string
}

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

func CanonicalUploadPayload(sessionID string, file SignedFile) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(canonicalPayload{
		SessionID: sessionID,
		File: canonicalFile{
			Key:      file.Key,
			Name:     file.Name,
			Size:     file.Size,
			MimeType: file.MimeType,
		},
	})
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func signUpload(secret, sessionID string, file SignedFile) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(CanonicalUploadPayload(sessionID, file))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyUpload(secret, sessionID string, file SignedFile, signature string) bool {
	expected := signUpload(secret, sessionID, file)
	return hmac.Equal([]byte(expected), []byte(signature))
}
