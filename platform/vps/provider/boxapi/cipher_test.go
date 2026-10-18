package boxapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
}

func TestAValueTheBoxSealsOpensUnderItsOwnAssociatedDataAndNoOther(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(KeyPath(root, environment.TierProduction)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(KeyPath(root, environment.TierProduction), aKey(t), 0o400); err != nil {
		t.Fatal(err)
	}
	vault := Cipher{Root: root}
	ctx := context.Background()

	sealed, err := vault.Seal(ctx, environment.TierProduction, bound, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != sealNonce+len("hunter2")+sealTag {
		t.Errorf("Seal() = %d bytes, want the nonce, the ciphertext and the tag: %d", len(sealed), sealNonce+len("hunter2")+sealTag)
	}
	again, err := vault.Seal(ctx, environment.TierProduction, bound, []byte("hunter2"))
	if err != nil || string(again[:sealNonce]) == string(sealed[:sealNonce]) {
		t.Errorf("two seals share the nonce %x, %v, and AES-GCM under one key never reuses one", sealed[:sealNonce], err)
	}
	opened, err := vault.Open(ctx, environment.TierProduction, bound, sealed)
	if err != nil || string(opened) != "hunter2" {
		t.Fatalf("Open(Seal()) = %q, %v", opened, err)
	}
	elsewhere := seal.BindToProject("other", "variable", "production", "*", "/", "DATABASE_URL")
	if _, err := vault.Open(ctx, environment.TierProduction, elsewhere, sealed); err == nil {
		t.Error("a value sealed for shop opened for other")
	}
	if _, err := vault.Seal(ctx, environment.TierPreview, bound, []byte("hunter2")); err == nil {
		t.Error("the preview tier sealed with no key of its own")
	}
	if _, err := vault.Seal(ctx, "", bound, []byte("hunter2")); err == nil {
		t.Error("a value sealed under no tier")
	}
}

func TestASealIsTheNonceThenTheCiphertextThenTheTag(t *testing.T) {
	t.Parallel()
	key, err := base64.StdEncoding.DecodeString(keySealedWith)
	if err != nil {
		t.Fatal(err)
	}
	want, err := base64.StdEncoding.DecodeString(valueSealedAlready)
	if err != nil {
		t.Fatal(err)
	}
	at := seal.BindToProject("shop", "variable", "production", "staging", "/web", "STRIPE_API_KEY")
	sealed, err := sealWithNonce(key, want[:sealNonce], at.Bytes(), []byte("sk_live_secret"))
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.StdEncoding.EncodeToString(sealed); got != valueSealedAlready {
		t.Errorf("sealWithNonce() = %s, want %s, the bytes the box has always opened", got, valueSealedAlready)
	}
}

func TestCreatingATierKeyRefusesOneThatExistsAndLeavesItReadableByItsOwnerAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := KeyPath(root, environment.TierProduction)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".staged", []byte("left by a crash"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CreateKey(root, environment.TierProduction); err != nil {
		t.Fatalf("CreateKey() = %v", err)
	}
	created, err := os.ReadFile(path)
	if err != nil || len(created) != sealKeyBytes {
		t.Fatalf("the created key is %d bytes, %v, want %d", len(created), err, sealKeyBytes)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Errorf("the created key is mode %v, %v, want 0400", info.Mode().Perm(), err)
	}
	if _, err := os.Lstat(path + ".staged"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staging file is still beside the key: %v", err)
	}

	if err := CreateKey(root, environment.TierProduction); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("CreateKey() over a key = %v, want it refused", err)
	}
	kept, err := os.ReadFile(path)
	if err != nil || string(kept) != string(created) {
		t.Error("a refused CreateKey() replaced the key every sealed value opens under")
	}
}
