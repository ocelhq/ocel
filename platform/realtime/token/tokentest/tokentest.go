package tokentest

import (
	"crypto/ed25519"
	"testing"

	"github.com/ocelhq/ocel/platform/realtime/token"
)

func Sign(t testing.TB, key ed25519.PrivateKey, claims token.Claims) string {
	t.Helper()
	minted, err := token.Sign(key, claims)
	if err != nil {
		t.Fatalf("sign %v: %v", claims, err)
	}
	return minted
}
