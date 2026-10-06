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
	"math/rand/v2"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

var isrWriterTimeout = 30 * time.Second

var isrWriterDestroyCalls = 400

var isrWriterBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

var isrWriterClient = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}

type ISRWriter struct {
	Endpoint            string
	BootstrapCredential string
	Seed                string
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

func (w ISRWriter) isReachable() bool {
	return w.Endpoint != "" && w.BootstrapCredential != ""
}

func (w ISRWriter) request(ctx context.Context, isrPrefix, operation string, body any) error {
	if !w.isReachable() {
		return nil
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal isr writer %s body: %w", operation, err)
		}
		reader = bytes.NewReader(encoded)
	}

	ctx, cancel := context.WithTimeout(ctx, isrWriterTimeout)
	defer cancel()

	url := w.Endpoint + "/" + isrPrefix + "/" + operation
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reader)
	if err != nil {
		return fmt.Errorf("build isr writer %s request: %w", operation, err)
	}
	req.Header.Set("Authorization", "Bearer "+w.BootstrapCredential)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := isrWriterClient.Do(req)
	if err != nil {
		return fmt.Errorf("call isr writer %s for %s: %w", operation, isrPrefix, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		respBody, _ := io.ReadAll(res.Body)
		return fmt.Errorf("isr writer %s for %s: status %d: %s", operation, isrPrefix, res.StatusCode, string(respBody))
	}
	return nil
}

func (w ISRWriter) PutEntry(ctx context.Context, isrPrefix, key string, body []byte) error {
	if w.Endpoint == "" || w.Seed == "" {
		return fmt.Errorf("the isr-writer for %s is not adopted; re-run `ocel bootstrap`", isrPrefix)
	}
	url := w.Endpoint + "/" + isrPrefix + "/entry?key=" + neturl.QueryEscape(key)
	for attempt := 0; ; attempt++ {
		status, err := w.putEntryOnce(ctx, url, isrPrefix, body)
		if err != nil {
			return fmt.Errorf("put entry %s of %s into the isr-writer: %w", key, isrPrefix, err)
		}
		switch {
		case status >= 200 && status < 300:
			return nil
		case status == http.StatusTooManyRequests && attempt < len(isrWriterBackoff):
			backoff := isrWriterBackoff[attempt]
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff/2 + rand.N(backoff/2+1)):
			}
		default:
			return fmt.Errorf("isr writer put of entry %s of %s: status %d", key, isrPrefix, status)
		}
	}
}

func (w ISRWriter) putEntryOnce(ctx context.Context, url, isrPrefix string, body []byte) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, isrWriterTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+DeriveISRWriteSecret(w.Seed, isrPrefix))
	req.Header.Set("Content-Type", "application/json")
	res, err := isrWriterClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, nil
}

func (w ISRWriter) Destroy(ctx context.Context, isrPrefix string) error {
	if !w.isReachable() {
		return nil
	}
	prefix := strings.Trim(isrPrefix, "/")
	url := w.Endpoint + "/" + prefix + "/destroy"
	for range isrWriterDestroyCalls {
		status, body, err := w.destroyOnce(ctx, url)
		if err != nil {
			return fmt.Errorf("isr writer destroy for %s: %w", prefix, err)
		}
		switch {
		case status == http.StatusAccepted:
			continue
		case status >= 200 && status < 300:
			return nil
		}
		return fmt.Errorf("isr writer destroy for %s: status %d: %s", prefix, status, body)
	}
	return fmt.Errorf("the isr writer still holds objects under %s after %d calls; prune again to finish", prefix, isrWriterDestroyCalls)
}

func (w ISRWriter) destroyOnce(ctx context.Context, url string) (int, string, error) {
	for attempt := 0; ; attempt++ {
		status, header, body, err := w.destroyAttempt(ctx, url)
		retryable := status == http.StatusTooManyRequests || status >= http.StatusInternalServerError || err != nil
		if !retryable || attempt >= storeMaxAttempts-1 {
			return status, body, err
		}
		delay := storeRetryDelay(&http.Response{StatusCode: status, Header: header}, attempt, retryJitter())
		if err := waitBeforeRetry(ctx, delay); err != nil {
			return 0, "", err
		}
	}
}

func (w ISRWriter) destroyAttempt(ctx context.Context, url string) (int, http.Header, string, error) {
	ctx, cancel := context.WithTimeout(ctx, isrWriterTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, nil, "", fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.BootstrapCredential)
	res, err := isrWriterClient.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
	return res.StatusCode, res.Header, string(body), nil
}
