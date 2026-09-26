package provider_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func TestParseNamespaceDefaultsToOcel(t *testing.T) {
	ns, err := provider.ParseNamespace("")
	if err != nil {
		t.Fatalf("an empty namespace: %v", err)
	}
	if ns != provider.DefaultNamespace {
		t.Errorf("ParseNamespace(%q) = %q, want %q", "", ns, provider.DefaultNamespace)
	}
}

func TestParseNamespaceAccepts(t *testing.T) {
	for _, given := range []string{"ocel", "j-a1b2c3-cache", "a", strings.Repeat("a", provider.MaxNamespaceLength)} {
		if _, err := provider.ParseNamespace(given); err != nil {
			t.Errorf("ParseNamespace(%q): %v", given, err)
		}
	}
}

func TestParseNamespaceRefuses(t *testing.T) {
	for _, given := range []string{"Ocel", "ocel_one", "1ocel", "-ocel", "ocel bootstrap", "ocel/one", strings.Repeat("a", provider.MaxNamespaceLength+1)} {
		if _, err := provider.ParseNamespace(given); err == nil {
			t.Errorf("ParseNamespace(%q) was accepted, want a refusal", given)
		}
	}
}

func TestParseNamespaceRefusesASpellingNamingWouldNotMint(t *testing.T) {
	for _, given := range []string{"a--b", "abc-", "a---b", "ocel--two"} {
		ns, err := provider.ParseNamespace(given)
		if err == nil {
			t.Errorf("ParseNamespace(%q) = %q, want a refusal: it is not the spelling a name minted from it would carry", given, ns)
		}
	}
}

func TestEveryAcceptedNamespaceIsTheFieldANameMintsFromIt(t *testing.T) {
	for _, given := range []string{"ocel", "j-a1b2c3-cache", "a", "a1"} {
		ns, err := provider.ParseNamespace(given)
		if err != nil {
			t.Fatalf("ParseNamespace(%q): %v", given, err)
		}
		if minted := naming.Join(naming.FieldSeparator, ns.String()); minted != ns.String() {
			t.Errorf("a name minted from namespace %q carries the field %q, so what is minted and what is matched differ", ns, minted)
		}
	}
}

func TestNamespaceFromEnvReadsTheVariableEveryProviderShares(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "j-a1b2c3")

	ns, err := provider.NamespaceFromEnv()
	if err != nil {
		t.Fatalf("NamespaceFromEnv() = %v", err)
	}
	if ns.String() != "j-a1b2c3" {
		t.Errorf("NamespaceFromEnv() = %q, want the namespace the environment names", ns)
	}
}

func TestNamespaceFromEnvRefusesNamingTheVariable(t *testing.T) {
	t.Setenv(provider.NamespaceEnvVar, "Not A Namespace")

	var refused refusal.Refusal
	_, err := provider.NamespaceFromEnv()
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("NamespaceFromEnv() = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, provider.NamespaceEnvVar) {
		t.Errorf("NamespaceFromEnv() refused with %q, want it to name %s", refused.Message, provider.NamespaceEnvVar)
	}
}
