package bootstrap

import (
	"context"
	"encoding/hex"
	"slices"
	"testing"
)

func TestEnsureOriginSecret(t *testing.T) {
	t.Run("is create only", func(t *testing.T) {
		ssmc := newFakeSSM()

		first, err := ensureOriginSecret(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ensureOriginSecret: %v", err)
		}
		if _, err := hex.DecodeString(first); err != nil || len(first) != 64 {
			t.Fatalf("secret = %q, want 32 random bytes the front can carry as a header", first)
		}

		again, err := ensureOriginSecret(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ensureOriginSecret (second run): %v", err)
		}
		if again != first {
			t.Errorf("second bootstrap returned %q, want the stored secret; rotating it strands every promoted pointer", again)
		}

		preview, err := ensureOriginSecret(context.Background(), ssmc, ClassPreview)
		if err != nil {
			t.Fatalf("ensureOriginSecret (preview): %v", err)
		}
		if preview == first {
			t.Error("preview and production share a secret; a preview front must not reach a production release")
		}
	})

	t.Run("converges on a concurrent bootstrap", func(t *testing.T) {
		ssmc := &racingSSM{fakeSSM: newFakeSSM(), winner: "the-other-bootstraps-secret"}

		secret, err := ensureOriginSecret(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ensureOriginSecret lost a race instead of converging: %v", err)
		}
		if secret != ssmc.winner {
			t.Errorf("secret = %q, want the winner's %q", secret, ssmc.winner)
		}
	})

	t.Run("refuses a class it has no parameter for", func(t *testing.T) {
		if _, err := ensureOriginSecret(context.Background(), newFakeSSM(), "staging"); err == nil {
			t.Error("an unknown substrate class minted a secret, want the class refused")
		}
	})
}

func TestBootstrapParamsCarryTheOriginSecret(t *testing.T) {
	params := fullProductionParams()
	params[OriginSecretParamName] = "origin-1"

	got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, "proj-1")
	if err != nil {
		t.Fatalf("ReadClassParams: %v", err)
	}
	if got.OriginSecret != "origin-1" {
		t.Errorf("OriginSecret = %q, want the secret bootstrap minted", got.OriginSecret)
	}

	names, err := ClassParamNames(ClassProduction)
	if err != nil {
		t.Fatalf("ClassParamNames: %v", err)
	}
	if !slices.Contains(names, OriginSecretParamName) {
		t.Errorf("teardown deletes %v, want the origin secret among them", names)
	}
}
