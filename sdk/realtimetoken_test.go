package ocel

import (
	"strings"
	"testing"
)

func TestEveryMintVectorSignsToItsTokenByteForByte(t *testing.T) {
	tokens := readRealtimeVectors(t).Tokens
	for _, vector := range tokens.Mint {
		token, err := signToken(tokens.SigningKey, vector.Header, vector.Claims)
		if err != nil {
			t.Fatalf("%s: %v", vector.Name, err)
		}
		if token != vector.Token {
			t.Errorf("%s: token = %q, want %q", vector.Name, token, vector.Token)
		}
	}
}

func TestATokenSignsLineAndParagraphSeparatorsUnescapedAsTheTypeScriptSDKDoes(t *testing.T) {
	const signedInput = "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJhdWQiOiJydC5leGFtcGxlIiwiZXhwIjoxNzAwMDAwMDYwLCJpYXQiOjE3MDAwMDAwMDAsImlzcyI6Im9jZWw6cnQ6YXBwIiwianRpIjoiQUFBQUFBQUFBQUFBQUFBQUFBQUFBQSIsIm9jZWwiOnsiY2giOiIvYXBwL3giLCJucyI6ImFwcCIsIm9wIjoic3Vic2NyaWJlIn0sInN1YiI6ImxpbmXigKhwYXJh4oCpZW5kIn0"
	token, err := signToken(readRealtimeVectors(t).Tokens.SigningKey, map[string]any{"alg": "EdDSA", "typ": "JWT"}, map[string]any{
		"iss":  "ocel:rt:app",
		"aud":  "rt.example",
		"sub":  "line\xe2\x80\xa8para\xe2\x80\xa9end",
		"iat":  1700000000,
		"exp":  1700000060,
		"jti":  "AAAAAAAAAAAAAAAAAAAAAA",
		"ocel": map[string]any{"op": "subscribe", "ch": "/app/x", "ns": "app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, signedInput+".") {
		t.Errorf("token = %q, want it to sign %q", token, signedInput)
	}
}
