package kvstore

import (
	"strings"
	"testing"
)

func TestAnEntryNameEverySDKCanHoldIsAccepted(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"session", "requests", "featureFlags", "rate_limit", "v2", strings.Repeat("a", 63)} {
		if err := RefuseEntryName(name); err != nil {
			t.Errorf("RefuseEntryName(%q) = %v, want it accepted", name, err)
		}
	}
}

func TestAnEntryNameAStoreAlreadyUsesIsRefusedNamingTheLanguage(t *testing.T) {
	t.Parallel()

	for name, says := range map[string]string{
		"client":            "every SDK",
		"connectionString":  "TypeScript",
		"connection_string": "Python and Rust",
		"then":              "TypeScript",
		"constructor":       "TypeScript",
	} {
		err := RefuseEntryName(name)
		if err == nil || !strings.Contains(err.Error(), "reserved") || !strings.Contains(err.Error(), says) {
			t.Errorf("RefuseEntryName(%q) = %v, want it refused as reserved by %s", name, err, says)
		}
	}
}

func TestAnEntryNameNoSDKCanHoldIsRefused(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "user-sessions", "1st", "_private", "has space", strings.Repeat("a", 64)} {
		err := RefuseEntryName(name)
		if err == nil || !strings.Contains(err.Error(), "starts with a letter") {
			t.Errorf("RefuseEntryName(%q) = %v, want it refused naming what an entry name is", name, err)
		}
	}
}
