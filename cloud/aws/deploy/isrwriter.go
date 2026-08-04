package deploy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// The deploy host's half of the ISR writer contract (workers/isr-writer):
// minting each build's write secret, seeding its hash into the writer's
// per-deploy Durable Object, and retiring it when the build is pruned.

// MintISRWriterSeed generates the per-deploy-run seed every app's write secret
// is derived from. It is never persisted: a rerun mints a new seed, derives new
// secrets, and reseeds each build's registry with their hashes.
func MintISRWriterSeed() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// isrWriteSecret derives one app's write secret from the deploy's seed and that
// app's isrPrefix. Deriving rather than minting keeps appCaches a pure function
// of the deploy — it is called several times per run and every call must agree
// — while still giving each app a secret that authorizes writes to its slice
// alone.
func isrWriteSecret(seed, isrPrefix string) string {
	mac := hmac.New(sha256.New, []byte(seed))
	mac.Write([]byte(isrPrefix))
	return hex.EncodeToString(mac.Sum(nil))
}

// isrWriteSecretHash is what the writer worker stores: the plaintext secret
// never leaves the deploy host and the Lambda it is injected into.
func isrWriteSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// seedISRWriters seeds every cached app's write-secret hash into the writer, so
// each build's functions can write the moment they go live. A substrate that
// adopted no writer seeds nothing.
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

// initializeISRWriter seeds one build's write-secret hash into the writer,
// authenticated with the account-level bootstrap credential. It must land
// before that build's functions serve a request, or their first entry write is
// rejected.
func initializeISRWriter(ctx context.Context, cfg Config, isrPrefix, secret string) error {
	body := map[string]string{"secretHash": isrWriteSecretHash(secret)}
	return isrWriterRequest(ctx, cfg, isrPrefix, "initialize", body)
}

// retireISRWriter drops one build's write secret when the build is pruned
// (epic decision 6d), alongside the entries it wrote.
func retireISRWriter(ctx context.Context, cfg Config, isrPrefix string) error {
	return isrWriterRequest(ctx, cfg, isrPrefix, "destroy", nil)
}

// isrWriterReachable reports whether this Config can reach the writer at all. A
// bootstrap predating it leaves the coordinates empty, and every writer call is
// then a no-op. It is all a retirement needs: destroy is authorized by the
// bootstrap credential, not by any deploy's secret, so a prune (which mints no
// seed) still retires what it reclaims.
func isrWriterReachable(cfg Config) bool {
	return cfg.ISRWriterEndpoint != "" && cfg.ISRWriterBootstrapCred != ""
}

// isrWriterConfigured additionally requires the per-run seed, which only a
// deploy has: without it there is no secret to derive, so nothing to point a
// function at and nothing to seed.
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
