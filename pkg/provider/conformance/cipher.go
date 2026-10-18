package conformance

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/seal"
)

func RunCipher(t *testing.T, cipher seal.Cipher) {
	t.Helper()

	ctx := context.Background()

	tier := environment.TierProduction
	bound := seal.AssociatedData{
		{Name: "project", Value: "shop"},
		{Name: "environment", Value: "*"},
		{Name: "folder", Value: "/"},
		{Name: "binding", Value: ""},
		{Name: "key", Value: "DATABASE_URL"},
	}
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
	for i, field := range bound {
		t.Run("a value sealed here does not open with another "+field.Name, func(t *testing.T) {
			moved := slices.Clone(bound)
			moved[i].Value = field.Value + "moved"
			if _, err := cipher.Open(ctx, tier, moved, sealed); err == nil {
				t.Fatalf("Open() with %s %q succeeded, so the associated data is not authenticated", field.Name, moved[i].Value)
			}
		})
	}
}
