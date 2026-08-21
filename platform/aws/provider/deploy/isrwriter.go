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

func checkISRWriterAgrees(stores ObjectStores, w ISRWriterAccess) error {
	adopted, writer := isrEntriesAdopted(stores), isrWriterConfigured(w)
	switch {
	case adopted && !writer:
		return fmt.Errorf("this bootstrap adopted an edge cache store but no ISR writer to write into it, so this build could not revalidate anything it cached; re-run `ocel bootstrap`")
	case !adopted && writer:
		return fmt.Errorf("this bootstrap adopted an ISR writer but no edge cache store, so entries would be written where nothing reads them; re-run `ocel bootstrap`")
	}
	return nil
}

func seedISRWriters(ctx context.Context, w ISRWriterAccess, caches map[string]*isrConfig) error {
	if !isrWriterConfigured(w) {
		return nil
	}
	for app, cache := range caches {
		secret := isrWriteSecret(w.Seed, cache.Prefix)
		if err := initializeISRWriter(ctx, w, cache.Prefix, secret); err != nil {
			return fmt.Errorf("seed the isr writer for %s: %w", app, err)
		}
	}
	return nil
}

func initializeISRWriter(ctx context.Context, w ISRWriterAccess, isrPrefix, secret string) error {
	body := map[string]string{"secretHash": isrWriteSecretHash(secret)}
	return isrWriterRequest(ctx, w, isrPrefix, "initialize", body)
}

func retireISRWriter(ctx context.Context, w ISRWriterAccess, isrPrefix string) error {
	return isrWriterRequest(ctx, w, isrPrefix, "destroy", nil)
}

func isrWriterReachable(w ISRWriterAccess) bool {
	return w.Endpoint != "" && w.BootstrapCred != ""
}

func isrWriterConfigured(w ISRWriterAccess) bool {
	return isrWriterReachable(w) && w.Seed != ""
}

func isrWriterRequest(ctx context.Context, w ISRWriterAccess, isrPrefix, op string, body any) error {
	if !isrWriterReachable(w) {
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
