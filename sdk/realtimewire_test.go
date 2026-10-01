package ocel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type realtimeVectors struct {
	Wire []struct {
		Name      string            `json:"name"`
		Namespace string            `json:"namespace"`
		Pattern   string            `json:"pattern"`
		Wildcard  bool              `json:"wildcard"`
		Params    map[string]string `json:"params"`
		Channel   string            `json:"channel"`
		Error     string            `json:"error"`
	} `json:"wire"`
	Tokens struct {
		SigningKey []byte `json:"signingKey"`
		VerifyKey  []byte `json:"verifyKey"`
		Mint       []struct {
			Name   string         `json:"name"`
			Header map[string]any `json:"header"`
			Claims map[string]any `json:"claims"`
			Token  string         `json:"token"`
		} `json:"mint"`
	} `json:"tokens"`
}

func readRealtimeVectors(t *testing.T) realtimeVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "proto", "realtime", "vectors.json"))
	if err != nil {
		t.Fatalf("read the shared realtime vectors: %v", err)
	}
	var vectors realtimeVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("decode the shared realtime vectors: %v", err)
	}
	return vectors
}

func TestEveryWireVectorEncodesToItsChannelOrIsRefusedForItsReason(t *testing.T) {
	for _, vector := range readRealtimeVectors(t).Wire {
		pattern, err := parseChannelPattern(vector.Pattern)
		if err != nil {
			t.Fatalf("%s: parse %q: %v", vector.Name, vector.Pattern, err)
		}
		channel, refused := encodeWireChannel(vector.Namespace, pattern, vector.Params, vector.Wildcard)
		if channel != vector.Channel || string(refused) != vector.Error {
			t.Errorf("%s: channel %q refused %q, want channel %q refused %q", vector.Name, channel, refused, vector.Channel, vector.Error)
		}
	}
}

func TestAChannelPatternOutsideTheGrammarIsRefusedSayingWhy(t *testing.T) {
	for written, reason := range map[string]string{
		"":              "empty",
		"a/b/c/d/e":     "at most 4",
		"orders//x":     "empty segment",
		"orders/:1x":    "no parameter",
		"orders_x":      "no literal",
		"rooms/:id/:id": "twice",
	} {
		if _, err := parseChannelPattern(written); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("parseChannelPattern(%q) = %v, want it refused for %q", written, err, reason)
		}
	}
}
