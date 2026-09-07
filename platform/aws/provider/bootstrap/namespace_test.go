package bootstrap

import (
	"strings"
	"testing"
)

func TestParseNamespaceDefaultsToOcel(t *testing.T) {
	ns, err := ParseNamespace("")
	if err != nil {
		t.Fatalf("an empty namespace: %v", err)
	}
	if ns != DefaultNamespace {
		t.Errorf("ParseNamespace(%q) = %q, want %q", "", ns, DefaultNamespace)
	}
}

func TestParseNamespaceAccepts(t *testing.T) {
	for _, given := range []string{"ocel", "j-a1b2c3-cache", "a", strings.Repeat("a", MaxNamespaceLength)} {
		if _, err := ParseNamespace(given); err != nil {
			t.Errorf("ParseNamespace(%q): %v", given, err)
		}
	}
}

func TestParseNamespaceRefuses(t *testing.T) {
	for _, given := range []string{"Ocel", "ocel_one", "1ocel", "-ocel", "ocel bootstrap", "ocel/one", strings.Repeat("a", MaxNamespaceLength+1)} {
		if _, err := ParseNamespace(given); err == nil {
			t.Errorf("ParseNamespace(%q) was accepted, want a refusal", given)
		}
	}
}
