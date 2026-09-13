package connectorkit

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

type sent struct {
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

func beating(t *testing.T, origin string) (held *beat, keyPath, configPath string) {
	t.Helper()
	root := t.TempDir()
	keyPath = filepath.Join(root, "key")
	configPath = filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := LoadOrCreateIdentity(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return &beat{
		connectorID:  "conn-1",
		version:      "0.0.0-alpha",
		capabilities: []string{CapabilityEnvVarsRead},
		identity:     identity,
		keyPath:      keyPath,
		configPath:   configPath,
		client:       http.DefaultClient,
		every:        time.Millisecond,
		origin:       origin,
	}, keyPath, configPath
}

func console(t *testing.T, answer func(seen int) int) (*httptest.Server, *atomic.Int64, *[]sent) {
	t.Helper()
	var count atomic.Int64
	bodies := &[]sent{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := int(count.Add(1))
		var body sent
		_ = json.NewDecoder(r.Body).Decode(&body)
		*bodies = append(*bodies, body)
		w.WriteHeader(answer(seen))
	}))
	t.Cleanup(server.Close)
	return server, &count, bodies
}

func TestHeartbeatCarriesASelfSignedToken(t *testing.T) {
	var carried string
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		carried = r.Header.Get("Authorization")
		path = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	held, _, _ := beating(t, server.URL)
	status, err := held.once(context.Background())
	if err != nil {
		t.Fatalf("once: %v", err)
	}
	if status != http.StatusNoContent {
		t.Fatalf("status = %d", status)
	}
	if path != "/api/connectors/conn-1/heartbeat" {
		t.Fatalf("path = %q", path)
	}

	raw, bearing := strings.CutPrefix(carried, "Bearer ")
	if !bearing {
		t.Fatalf("authorization = %q", carried)
	}
	public, err := base64.StdEncoding.DecodeString(held.identity.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.ParseString(raw,
		jwt.WithKey(jwa.EdDSA(), ed25519.PublicKey(public)),
		jwt.WithIssuer("conn-1"),
		jwt.WithSubject("conn-1"),
		jwt.WithAudience(server.URL),
	)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	expires, _ := token.Expiration()
	if window := time.Until(expires); window > beatLifetime+time.Second {
		t.Fatalf("the token lives for %s, longer than %s", window, beatLifetime)
	}
}

func TestHeartbeatWipesAfterA404ThatFollowsASuccess(t *testing.T) {
	server, count, bodies := console(t, func(seen int) int {
		if seen == 1 {
			return http.StatusNoContent
		}
		return http.StatusNotFound
	})

	held, keyPath, configPath := beating(t, server.URL)
	retired := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held.run(ctx, retired)

	select {
	case <-retired:
	default:
		t.Fatal("the connector did not retire")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("the key is still there: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("the config is still there: %v", err)
	}
	if count.Load() != 2 {
		t.Fatalf("the console saw %d beats, want 2", count.Load())
	}
	if len(*bodies) == 0 || (*bodies)[0].Version != "0.0.0-alpha" ||
		len((*bodies)[0].Capabilities) != 1 || (*bodies)[0].Capabilities[0] != CapabilityEnvVarsRead {
		t.Fatalf("the beat carried %+v", *bodies)
	}
}

func TestHeartbeatKeepsTheKeyWhenThe404ComesFirst(t *testing.T) {
	server, count, _ := console(t, func(int) int { return http.StatusNotFound })

	held, keyPath, configPath := beating(t, server.URL)
	retired := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	held.run(ctx, retired)

	select {
	case <-retired:
		t.Fatal("a 404 before any success retired the connector")
	default:
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("the key was wiped: %v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("the config was wiped: %v", err)
	}
	if count.Load() < 2 {
		t.Fatalf("the connector stopped beating after %d", count.Load())
	}
}

func TestHeartbeatKeepsGoingThroughOtherStatuses(t *testing.T) {
	server, count, _ := console(t, func(int) int { return http.StatusInternalServerError })

	held, keyPath, _ := beating(t, server.URL)
	retired := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	held.run(ctx, retired)

	if count.Load() < 2 {
		t.Fatalf("the connector stopped beating after %d", count.Load())
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("the key was wiped: %v", err)
	}
}
