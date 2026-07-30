package baked

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func key(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeyBytes)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

// TestSealOpen_RoundTripsAndHidesThePlaintext proves the two halves of the
// format agree, and that the bytes that ride inside the bundle disclose
// nothing on their own: neither a value nor the name it is stored under
// survives into the sealed form.
func TestSealOpen_RoundTripsAndHidesThePlaintext(t *testing.T) {
	values := map[string]string{"STRIPE_API_KEY": "sk-live-abc", "WEBHOOK_SECRET": "whsec-xyz"}
	k := key(t)

	sealed, err := Seal(k, values)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for _, secret := range []string{"sk-live-abc", "whsec-xyz", "STRIPE_API_KEY"} {
		if bytes.Contains(sealed, []byte(secret)) {
			t.Errorf("sealed bytes carry %q in the clear", secret)
		}
	}

	opened, err := Open(k, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for name, want := range values {
		if opened[name] != want {
			t.Errorf("Open()[%q] = %q, want %q", name, opened[name], want)
		}
	}
	if len(opened) != len(values) {
		t.Errorf("Open() = %v, want exactly what was sealed", opened)
	}
}

// TestSeal_NeverRepeatsItsBytes proves one payload never seals to one blob:
// the artifact a rotation ships must differ from the one before it even when a
// value returns to a previous setting, and a repeated (key, nonce) pair would
// break GCM outright.
func TestSeal_NeverRepeatsItsBytes(t *testing.T) {
	k := key(t)
	values := map[string]string{"A": "one"}

	first, err := Seal(k, values)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	second, err := Seal(k, values)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Error("two seals of one payload produced identical bytes")
	}
}

// TestOpen_RefusesTheWrongKeyAndTamperedBytes proves a bundle that cannot be
// opened is an error rather than an empty set of values: serving a request
// with a variable silently unset is the one outcome the class must never
// produce.
func TestOpen_RefusesTheWrongKeyAndTamperedBytes(t *testing.T) {
	k := key(t)
	sealed, err := Seal(k, map[string]string{"A": "one"})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := Open(key(t), sealed); err == nil {
		t.Error("Open accepted a data key the bundle was not sealed under")
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := Open(k, tampered); err == nil {
		t.Error("Open accepted bytes that had been altered")
	}

	if _, err := Open(k, sealed[:NonceBytes-1]); err == nil {
		t.Error("Open accepted a bundle too short to hold a nonce")
	}
}

// TestSeal_RefusesAKeyOfTheWrongLength proves the format pins one cipher: a
// short key would otherwise fail at the runtime, on the far side of a deploy.
func TestSeal_RefusesAKeyOfTheWrongLength(t *testing.T) {
	if _, err := Seal(make([]byte, 16), map[string]string{"A": "one"}); err == nil {
		t.Error("Seal accepted a 16-byte data key")
	}
}

// TestPrefix_CannotCollideWithAUserChosenKey proves the name a value is
// injected under is one the SDK reserves, so an opened value can never be
// shadowed by something the process environment already carried.
func TestPrefix_CannotCollideWithAUserChosenKey(t *testing.T) {
	if !strings.HasPrefix(Prefix, "OCEL_") {
		t.Errorf("Prefix = %q, want a name the SDK's reserved prefixes cover", Prefix)
	}
}
