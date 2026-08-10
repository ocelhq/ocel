package deploy

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

func isrWriteSecret(seed, isrPrefix string) string {
	mac := hmac.New(sha256.New, []byte(seed))
	mac.Write([]byte(isrPrefix))
	return hex.EncodeToString(mac.Sum(nil))
}

func isrWriteSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func checkISRWriterAgrees(cfg Config) error {
	adopted, writer := isrEntriesAdopted(cfg), isrWriterConfigured(cfg)
	switch {
	case adopted && !writer:
		return fmt.Errorf("this substrate adopted an edge cache store but no ISR writer to write into it, so this build could not revalidate anything it cached; re-run `ocel bootstrap`")
	case !adopted && writer:
		return fmt.Errorf("this substrate adopted an ISR writer but no edge cache store, so entries would be written where nothing reads them; re-run `ocel bootstrap`")
	}
	return nil
}

func seedISRWriters(ctx context.Context, cfg Config, caches map[string]*isrConfig) error {
	if !isrWriterConfigured(cfg) {
		return nil
	}
	for app, cache := range caches {
		secret := isrWriteSecret(cfg.ISRWriterSeed, cache.Prefix)
		if err := initializeISRWriter(ctx, cfg, cache.Prefix, secret); err != nil {
			return fmt.Errorf("seed the isr writer for %s: %w", app, err)
		}
	}
	return nil
}

func initializeISRWriter(ctx context.Context, cfg Config, isrPrefix, secret string) error {
	body := map[string]string{"secretHash": isrWriteSecretHash(secret)}
	return isrWriterRequest(ctx, cfg, isrPrefix, "initialize", body)
}

func retireISRWriter(ctx context.Context, cfg Config, isrPrefix string) error {
	return isrWriterRequest(ctx, cfg, isrPrefix, "destroy", nil)
}

func isrWriterReachable(cfg Config) bool {
	return cfg.ISRWriterEndpoint != "" && cfg.ISRWriterBootstrapCred != ""
}

func isrWriterConfigured(cfg Config) bool {
	return isrWriterReachable(cfg) && cfg.ISRWriterSeed != ""
}

func isrWriterRequest(ctx context.Context, cfg Config, isrPrefix, op string, body any) error {
	if !isrWriterReachable(cfg) {
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

	url := cfg.ISRWriterEndpoint + "/" + isrPrefix + "/" + op
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, reader)
	if err != nil {
		return fmt.Errorf("build isr writer %s request: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.ISRWriterBootstrapCred)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := http.DefaultClient.Do(req)
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
