package bucket

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

func CanonicalUploadPayload(sessionID string, file SignedFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(canonicalPayload{SessionID: sessionID, File: canonicalFile(file)}); err != nil {
		return nil, fmt.Errorf("encode canonical upload payload: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func signUpload(secret, sessionID string, file SignedFile) (string, error) {
	payload, err := CanonicalUploadPayload(sessionID, file)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func verifyUpload(secret, sessionID string, file SignedFile, signature string) (bool, error) {
	expected, err := signUpload(secret, sessionID, file)
	if err != nil {
		return false, err
	}
	return hmac.Equal([]byte(expected), []byte(signature)), nil
}
