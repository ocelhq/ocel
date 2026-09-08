package providerkit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestParseNamespaceDefaultsToOcel(t *testing.T) {
	ns, err := providerkit.ParseNamespace("")
	if err != nil {
		t.Fatalf("an empty namespace: %v", err)
	}
	if ns != providerkit.DefaultNamespace {
		t.Errorf("ParseNamespace(%q) = %q, want %q", "", ns, providerkit.DefaultNamespace)
	}
}

func TestParseNamespaceAccepts(t *testing.T) {
	for _, given := range []string{"ocel", "j-a1b2c3-cache", "a", strings.Repeat("a", providerkit.MaxNamespaceLength)} {
		if _, err := providerkit.ParseNamespace(given); err != nil {
			t.Errorf("ParseNamespace(%q): %v", given, err)
		}
	}
}

func TestParseNamespaceRefuses(t *testing.T) {
	for _, given := range []string{"Ocel", "ocel_one", "1ocel", "-ocel", "ocel bootstrap", "ocel/one", strings.Repeat("a", providerkit.MaxNamespaceLength+1)} {
		if _, err := providerkit.ParseNamespace(given); err == nil {
			t.Errorf("ParseNamespace(%q) was accepted, want a refusal", given)
		}
	}
}

func TestNamespaceFromEnvReadsTheVariableEveryProviderShares(t *testing.T) {
	t.Setenv(providerkit.NamespaceEnvVar, "j-a1b2c3")

	ns, err := providerkit.NamespaceFromEnv()
	if err != nil {
		t.Fatalf("NamespaceFromEnv() = %v", err)
	}
	if ns.String() != "j-a1b2c3" {
		t.Errorf("NamespaceFromEnv() = %q, want the namespace the environment names", ns)
	}
}

func TestNamespaceFromEnvRefusesNamingTheVariable(t *testing.T) {
	t.Setenv(providerkit.NamespaceEnvVar, "Not A Namespace")

	var refusal providerkit.Refusal
	_, err := providerkit.NamespaceFromEnv()
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("NamespaceFromEnv() = %v, want an %s refusal", err, providerkit.CodeInvalid)
	}
	if !strings.Contains(refusal.Message, providerkit.NamespaceEnvVar) {
		t.Errorf("NamespaceFromEnv() refused with %q, want it to name %s", refusal.Message, providerkit.NamespaceEnvVar)
	}
}
