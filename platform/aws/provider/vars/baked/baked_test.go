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

func TestSealOpen(t *testing.T) {
	t.Parallel()

	t.Run("round trips and hides the plaintext", func(t *testing.T) {
		t.Parallel()

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
	})
}

func TestSeal(t *testing.T) {
	t.Parallel()

	t.Run("never repeats its bytes", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("refuses a key of the wrong length", func(t *testing.T) {
		t.Parallel()

		if _, err := Seal(make([]byte, 16), map[string]string{"A": "one"}); err == nil {
			t.Error("Seal accepted a 16-byte data key")
		}
	})
}

func TestOpen(t *testing.T) {
	t.Parallel()

	t.Run("refuses the wrong key and tampered bytes", func(t *testing.T) {
		t.Parallel()

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
	})
}

func TestPrefix(t *testing.T) {
	t.Parallel()

	t.Run("cannot collide with a user chosen key", func(t *testing.T) {
		t.Parallel()

		if !strings.HasPrefix(Prefix, "OCEL_") {
			t.Errorf("Prefix = %q, want a name the SDK's reserved prefixes cover", Prefix)
		}
	})
}
