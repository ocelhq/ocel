package conformance

import (
	"bytes"
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
)

func RunCipher(t *testing.T, cipher seal.Cipher) {
	t.Helper()

	ctx := context.Background()

	tier := environment.TierProduction
	bound := seal.BindToProject("shop", "conformance", "*", "/", "", "DATABASE_URL")
	plaintext := []byte("postgres://example")

	sealed, err := cipher.Seal(ctx, tier, bound, plaintext)
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("Seal() returned the plaintext inside its output")
	}

	opened, err := cipher.Open(ctx, tier, bound, sealed)
	if err != nil {
		t.Fatalf("Open() under the tier and associated data sealed = %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %q, want %q", opened, plaintext)
	}

	t.Run("a value sealed under one tier does not open under another", func(t *testing.T) {
		if _, err := cipher.Open(ctx, environment.TierPreview, bound, sealed); err == nil {
			t.Fatal("Open() under another tier succeeded, so one tier's key opens another's values")
		}
	})
	for name, moved := range map[string]seal.AssociatedData{
		"purpose":     seal.BindToProject("shop", "conformance moved", "*", "/", "", "DATABASE_URL"),
		"project":     seal.BindToProject("shopmoved", "conformance", "*", "/", "", "DATABASE_URL"),
		"first value": seal.BindToProject("shop", "conformance", "*moved", "/", "", "DATABASE_URL"),
		"empty value": seal.BindToProject("shop", "conformance", "*", "/", "moved", "DATABASE_URL"),
		"last value":  seal.BindToProject("shop", "conformance", "*", "/", "", "DATABASE_URLmoved"),
		"value count": seal.BindToProject("shop", "conformance", "*", "/", ""),
	} {
		t.Run("a value sealed here does not open with its "+name+" changed", func(t *testing.T) {
			if _, err := cipher.Open(ctx, tier, moved, sealed); err == nil {
				t.Fatalf("Open() bound to %s succeeded, so the associated data is not authenticated", moved.Bytes())
			}
		})
	}
}
