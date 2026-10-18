package boxapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
)

func sealFor(t *testing.T, key []byte, bound seal.AssociatedData, plaintext string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, sealNonce)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return append(nonce, gcm.Seal(nil, nonce, []byte(plaintext), bound.Bytes())...)
}

func aKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, sealKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

var bound = seal.BindToProject("shop", "variable", "production", "*", "/", "DATABASE_URL")

func TestAValueOpensUnderItsOwnAssociatedDataAndNoOther(t *testing.T) {
	t.Parallel()
	key := aKey(t)
	sealed := sealFor(t, key, bound, "postgres://example")

	opened, err := Open(key, bound, sealed)
	if err != nil || string(opened) != "postgres://example" {
		t.Fatalf("Open() = %q, %v", opened, err)
	}
	elsewhere := seal.BindToProject("other", "variable", "production", "*", "/", "DATABASE_URL")
	if _, err := Open(key, elsewhere, sealed); err == nil {
		t.Error("a value sealed for shop opened for other, so the associated data authenticates nothing")
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := Open(key, bound, sealed); err == nil {
		t.Error("a sealed value whose bytes moved opened anyway")
	}
	if _, err := Open(key[:16], bound, sealed); err == nil {
		t.Error("a key narrower than AES-256 opened something")
	}
}

const (
	keySealedWith      = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	valueSealedAlready = "oKGio6SlpqeoqaqrlXMjQSy9Z+ARAOShYg7UlbGBMatxpNpQTeD5bD/s"
)

func TestAValueOnTheBoxOpensUnderTheKeyAndAssociatedDataItWasSealedWith(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key, err := base64.StdEncoding.DecodeString(keySealedWith)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := base64.StdEncoding.DecodeString(valueSealedAlready)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "production"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "production", "seal.key"), key, 0o400); err != nil {
		t.Fatal(err)
	}
	at := seal.BindToProject("shop", "variable", "production", "staging", "/web", "STRIPE_API_KEY")
	opened, err := Cipher{Root: root}.Open(context.Background(), environment.TierProduction, at, sealed)
	if err != nil || string(opened) != "sk_live_secret" {
		t.Fatalf("Open() = %q, %v, want the value sealed as a variable of shop at production/staging//web/STRIPE_API_KEY to open: every value on a box is bound to those", opened, err)
	}
}

func TestTheBoxCipherReadsTheTierKeyWhereBootstrapMintsIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	key := aKey(t)
	if err := os.MkdirAll(filepath.Dir(KeyPath(root, environment.TierProduction)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(KeyPath(root, environment.TierProduction), key, 0o400); err != nil {
		t.Fatal(err)
	}
	vault := Cipher{Root: root}
	opened, err := vault.Open(context.Background(), environment.TierProduction, bound, sealFor(t, key, bound, "hunter2"))
	if err != nil || string(opened) != "hunter2" {
		t.Fatalf("Open() = %q, %v", opened, err)
	}
	if _, err := vault.Open(context.Background(), environment.TierPreview, bound, sealFor(t, key, bound, "hunter2")); err == nil {
		t.Error("a preview value opened under the production key, and each tier is sealed to its own")
	}
	if _, err := vault.Seal(context.Background(), environment.TierProduction, bound, []byte("x")); err == nil {
		t.Error("the box-side cipher sealed something, and sealing is the helper's under sudo alone")
	}
}
