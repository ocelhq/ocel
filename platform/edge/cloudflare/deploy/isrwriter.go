package cloudflare

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

var isrWriterClient = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}

type ISRWriter struct {
	Endpoint      string
	BootstrapCred string
	Seed          string
}

func DeriveISRWriteSecret(seed, isrPrefix string) string {
	mac := hmac.New(sha256.New, []byte(seed))
	mac.Write([]byte(isrPrefix))
	return hex.EncodeToString(mac.Sum(nil))
}

func isrWriteSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func (w ISRWriter) IsConfigured() bool {
	return w.isReachable() && w.Seed != ""
}

func (w ISRWriter) Initialize(ctx context.Context, isrPrefix string) error {
	if !w.IsConfigured() {
		return nil
	}
	body := map[string]string{"secretHash": isrWriteSecretHash(DeriveISRWriteSecret(w.Seed, isrPrefix))}
	return w.request(ctx, isrPrefix, "initialize", body)
}

func (w ISRWriter) Retire(ctx context.Context, isrPrefix string) error {
	return w.request(ctx, isrPrefix, "destroy", nil)
}

func (w ISRWriter) isReachable() bool {
	return w.Endpoint != "" && w.BootstrapCred != ""
}

func (w ISRWriter) request(ctx context.Context, isrPrefix, op string, body any) error {
	if !w.isReachable() {
		return nil
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal isr writer %s body: %w", op, err)
		}
		reader = bytes.NewReader(encoded)
	}

	url := w.Endpoint + "/" + isrPrefix + "/" + op
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reader)
	if err != nil {
		return fmt.Errorf("build isr writer %s request: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+w.BootstrapCred)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := isrWriterClient.Do(req)
	if err != nil {
		return fmt.Errorf("call isr writer %s for %s: %w", op, isrPrefix, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		respBody, _ := io.ReadAll(res.Body)
		return fmt.Errorf("isr writer %s for %s: status %d: %s", op, isrPrefix, res.StatusCode, string(respBody))
	}
	return nil
}
