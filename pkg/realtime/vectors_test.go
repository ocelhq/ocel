package realtime

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type vectors struct {
	Wire   []wireVector `json:"wire"`
	Tokens struct {
		SigningKey []byte `json:"signingKey"`
		VerifyKey  []byte `json:"verifyKey"`
		Now        int64  `json:"now"`
		Expect     struct {
			Aud string `json:"aud"`
			Ns  string `json:"ns"`
			Ch  string `json:"ch"`
		} `json:"expect"`
		Cases []tokenVector `json:"cases"`
		Mint  []mintVector  `json:"mint"`
	} `json:"tokens"`
}

type wireVector struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Pattern   string            `json:"pattern"`
	Wildcard  bool              `json:"wildcard"`
	Params    map[string]string `json:"params"`
	Channel   string            `json:"channel"`
	Error     string            `json:"error"`
}

type tokenVector struct {
	Name   string `json:"name"`
	Token  string `json:"token"`
	Valid  bool   `json:"valid"`
	Reason string `json:"reason"`
}

type mintVector struct {
	Name   string          `json:"name"`
	Header json.RawMessage `json:"header"`
	Claims json.RawMessage `json:"claims"`
	Token  string          `json:"token"`
}

func readVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "proto", "realtime", "vectors.json"))
	if err != nil {
		t.Fatalf("read the shared realtime vectors: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode the shared realtime vectors: %v", err)
	}
	return v
}

var lowerBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func encodedValue(value string) string {
	if channelSegment.MatchString(value) && !strings.HasPrefix(value, "0z") {
		return value
	}
	return "0z" + lowerBase32.EncodeToString([]byte(value))
}

func wireChannel(v wireVector) (string, string) {
	segments := strings.Split(v.Pattern, "/")
	for name := range v.Params {
		if !strings.Contains("/"+v.Pattern+"/", "/:"+name+"/") {
			return "", "unknown-param"
		}
	}
	channel := []string{"", v.Namespace}
	for _, s := range segments {
		name, isParam := strings.CutPrefix(s, ":")
		if !isParam {
			channel = append(channel, s)
			continue
		}
		value, given := v.Params[name]
		switch {
		case !given && v.Wildcard:
			for _, rest := range segments[len(channel)-2:] {
				after, isParam := strings.CutPrefix(rest, ":")
				if _, later := v.Params[after]; isParam && later {
					return "", "missing-param"
				}
			}
			return strings.Join(append(channel, "*"), "/"), ""
		case !given:
			return "", "missing-param"
		case value == "":
			return "", "empty-value"
		case len(value) > 30:
			return "", "value-too-long"
		}
		channel = append(channel, encodedValue(value))
	}
	return strings.Join(channel, "/"), ""
}

func TestEveryWireVectorEncodesAsTheEncodingRuleSays(t *testing.T) {
	t.Parallel()

	for _, v := range readVectors(t).Wire {
		channel, refused := wireChannel(v)
		if channel != v.Channel || refused != v.Error {
			t.Errorf("%s: %q with %v = channel %q error %q, want channel %q error %q", v.Name, v.Pattern, v.Params, channel, refused, v.Channel, v.Error)
		}
		if v.Channel == "" {
			continue
		}
		for _, segment := range strings.Split(strings.TrimPrefix(v.Channel, "/"), "/") {
			if segment != "*" && !channelSegment.MatchString(segment) {
				t.Errorf("%s: channel %q holds segment %q, which no transport accepts", v.Name, v.Channel, segment)
			}
		}
	}
}

func TestNoTwoWireVectorsOfOnePatternShareAChannel(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	for _, v := range readVectors(t).Wire {
		if v.Channel == "" {
			continue
		}
		if prior, taken := seen[v.Channel]; taken {
			t.Errorf("%q and %q both encode to %q, want the encoding injective", prior, v.Name, v.Channel)
		}
		seen[v.Channel] = v.Name
	}
}

type tokenClaims struct {
	Aud  string `json:"aud"`
	Exp  int64  `json:"exp"`
	Ocel struct {
		Ch string `json:"ch"`
		Ns string `json:"ns"`
	} `json:"ocel"`
}

func tokenRefusal(token string, verifyKey []byte, now int64, aud, ns, ch string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "malformed"
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "malformed"
	}
	var alg struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(header, &alg) != nil || alg.Alg != "EdDSA" {
		return "algorithm"
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(verifyKey, []byte(parts[0]+"."+parts[1]), signature) {
		return "signature"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims tokenClaims
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return "malformed"
	}
	switch {
	case now >= claims.Exp:
		return "expired"
	case claims.Aud != aud:
		return "audience"
	case claims.Ocel.Ns != ns:
		return "namespace"
	case claims.Ocel.Ch != ch:
		return "channel"
	}
	return ""
}

func TestEveryTokenVectorIsRefusedForTheReasonItNames(t *testing.T) {
	t.Parallel()

	tokens := readVectors(t).Tokens
	if !bytes.Equal(ed25519.NewKeyFromSeed(tokens.SigningKey).Public().(ed25519.PublicKey), tokens.VerifyKey) {
		t.Fatalf("the verify key is not the signing key's public key")
	}
	for _, c := range tokens.Cases {
		refused := tokenRefusal(c.Token, tokens.VerifyKey, tokens.Now, tokens.Expect.Aud, tokens.Expect.Ns, tokens.Expect.Ch)
		if c.Valid != (refused == "") || refused != c.Reason {
			t.Errorf("%s: refused for %q, want valid %v refused for %q", c.Name, refused, c.Valid, c.Reason)
		}
	}
}

func TestTheRealtimeBindingFixtureCarriesTheVectorsKeyPair(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "proto", "common", "bindings", "v1", "fixtures", "realtime.json"))
	if err != nil {
		t.Fatalf("read the realtime binding fixture: %v", err)
	}
	var fixture struct {
		Realtime struct {
			SigningKey []byte `json:"signingKey"`
			VerifyKey  []byte `json:"verifyKey"`
		} `json:"realtime"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode the realtime binding fixture: %v", err)
	}
	tokens := readVectors(t).Tokens
	if !bytes.Equal(fixture.Realtime.SigningKey, tokens.SigningKey) || !bytes.Equal(fixture.Realtime.VerifyKey, tokens.VerifyKey) {
		t.Error("the binding fixture's keys differ from the token vectors', want one key pair so a token minted from the fixture verifies against the vectors")
	}
}

func canonicalJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatalf("encode %s: %v", raw, err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

func TestEveryMintVectorIsTheTokenItsHeaderAndClaimsSignTo(t *testing.T) {
	t.Parallel()

	tokens := readVectors(t).Tokens
	if len(tokens.Mint) == 0 {
		t.Fatal("the token vectors hold no mint cases, want the exact token a minter produces pinned")
	}
	key := ed25519.NewKeyFromSeed(tokens.SigningKey)
	for _, c := range tokens.Mint {
		input := base64.RawURLEncoding.EncodeToString(canonicalJSON(t, c.Header)) + "." + base64.RawURLEncoding.EncodeToString(canonicalJSON(t, c.Claims))
		minted := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
		if minted != c.Token {
			t.Errorf("%s: minted %q, want %q", c.Name, minted, c.Token)
		}
	}
}
