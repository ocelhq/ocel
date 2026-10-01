package tokentest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/platform/realtime/token"
)

func Sign(t testing.TB, key ed25519.PrivateKey, claims token.Claims) string {
	t.Helper()
	input := segment(t, token.Header{Algorithm: token.Algorithm, Type: "JWT"}) + "." + segment(t, claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

func segment(t testing.TB, value any) string {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatalf("encode %v: %v", value, err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(out.Bytes(), []byte("\n")))
}
