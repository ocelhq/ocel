package main

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestNewListener(t *testing.T) {
	t.Run("refuses to start without the state table", func(t *testing.T) {
		_, err := newListener(context.Background(), env(map[string]string{sessionPrefixEnvVar: "PROJECT#shop#ENV#prod#SESSION#"}))
		if err == nil || !strings.Contains(err.Error(), stateTableEnvVar) {
			t.Fatalf("newListener = %v, want the missing table named", err)
		}
	})

	t.Run("refuses to start without the session prefix", func(t *testing.T) {
		_, err := newListener(context.Background(), env(map[string]string{stateTableEnvVar: "ocel-state"}))
		if err == nil || !strings.Contains(err.Error(), sessionPrefixEnvVar) {
			t.Fatalf("newListener = %v, want the missing prefix named rather than a listener that reads the shared key space", err)
		}
	})
}

func TestAllowedOrigins(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"https://a.example", []string{"https://a.example"}},
		{" https://a.example , https://b.example,", []string{"https://a.example", "https://b.example"}},
	} {
		if got := allowedOrigins(tc.raw); !slices.Equal(got, tc.want) {
			t.Errorf("allowedOrigins(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
