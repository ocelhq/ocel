package connectorkit

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateIdentityKeepsTheKeyItWrote(t *testing.T) {
	at := filepath.Join(t.TempDir(), "nested", "key")

	first, err := LoadOrCreateIdentity(at)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !first.HasKey() {
		t.Fatal("a created identity has no key")
	}

	info, err := os.Stat(at)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}

	again, err := LoadOrCreateIdentity(at)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.PublicKey() != first.PublicKey() {
		t.Fatalf("reload minted a second key: %s then %s", first.PublicKey(), again.PublicKey())
	}
}

func TestAnIdentityReadOutOfASecretStoreIsTheOneAFileWouldContain(t *testing.T) {
	at := filepath.Join(t.TempDir(), "key")
	filed, err := LoadOrCreateIdentity(at)
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(at)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(string(written[:len(written)-1]))
	if err != nil {
		t.Fatal(err)
	}

	stored, err := IdentityFromSeed(seed)
	if err != nil {
		t.Fatalf("IdentityFromSeed: %v", err)
	}
	if stored.PublicKey() != filed.PublicKey() {
		t.Fatalf("the seed names %s, the file %s: a host keeping the seed in its secret store must register the key the file would", stored.PublicKey(), filed.PublicKey())
	}
	if _, err := IdentityFromSeed(seed[:ed25519.SeedSize-1]); err == nil {
		t.Fatal("a short seed made an identity")
	}
}

func TestPublicKeyIsThirtyTwoRawBytes(t *testing.T) {
	identity, err := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(identity.PublicKey())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		t.Fatalf("public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
}

func TestLoadOrCreateIdentityRefusesAKeyOfTheWrongLength(t *testing.T) {
	at := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(at, []byte(base64.StdEncoding.EncodeToString([]byte("short"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(at); err == nil {
		t.Fatal("a five byte seed was accepted")
	}
}

func TestLoadOrCreateIdentityNeedsAPath(t *testing.T) {
	if _, err := LoadOrCreateIdentity(""); err == nil {
		t.Fatal("an empty path was accepted")
	}
}
