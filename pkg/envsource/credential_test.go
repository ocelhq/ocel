package envsource_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func scopeOf(project string) variablestore.Scope {
	return variablestore.Scope{Project: project, Tier: environment.TierProduction}
}

func setCredentials(t *testing.T, store variablestore.Store, project, clientID, clientSecret string) {
	t.Helper()
	for key, plaintext := range map[string]string{"INFISICAL_CLIENT_ID": clientID, "INFISICAL_CLIENT_SECRET": clientSecret} {
		if _, err := store.Set(context.Background(), scopeOf(project), tierWide("", key), plaintext, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnUnsetCredentialIsRefusedWithTheCommandThatSetsIt(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	descriptor := infisicalRegistration("shop", "https://infisical.example.com", universal, "").Descriptor
	for tier, flag := range map[environment.Tier]string{environment.TierProduction: "", environment.TierPreview: " --preview"} {
		_, err := envsource.ReadCredentials(context.Background(), store, variablestore.Scope{Project: "shop", Tier: tier}, descriptor)
		var refused *envsource.CredentialError
		if !errors.As(err, &refused) || refused.Variable != "INFISICAL_CLIENT_ID" || !strings.Contains(err.Error(), "ocel env set INFISICAL_CLIENT_ID=<VALUE>"+flag+"`") {
			t.Errorf("ReadCredentials() in %s = %v, want INFISICAL_CLIENT_ID refused with the command that sets it", tier, err)
		}
	}
}

func TestACredentialAnEnvSourceWroteIsRefused(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	descriptor := infisicalRegistration("shop", "https://infisical.example.com", universal, "").Descriptor
	setCredentials(t, store, "shop", "id", "secret")
	if _, err := envsource.ReadCredentials(ctx, store, scope, descriptor); err != nil {
		t.Fatalf("ReadCredentials() with both set = %v", err)
	}

	if _, err := store.SetFromEnvSource(ctx, scope, tierWide("", "INFISICAL_CLIENT_SECRET"), "cached", variablestore.Provenance{EnvSource: "infisical:p-1/prod", Version: "s9@1"}, 1); err != nil {
		t.Fatal(err)
	}
	_, err := envsource.ReadCredentials(ctx, store, scope, descriptor)
	if err == nil || !strings.Contains(err.Error(), "INFISICAL_CLIENT_SECRET") || !strings.Contains(err.Error(), "infisical:p-1/prod") {
		t.Fatalf("ReadCredentials() with a credential an env source wrote = %v, want it refused naming both", err)
	}
}

func TestACredentialReferencedFromAProjectMustBeOneThatProjectStoresItself(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	shared := scopeOf("shared")
	for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET", "OTHER"} {
		if _, err := store.Set(ctx, shared, tierWide("", key), "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"} {
		if _, err := store.SetReference(ctx, scope, tierWide("", key), variablestore.Target{Project: "shared", Cell: variablestore.Cell{Key: key}}); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := infisicalRegistration("shop", "https://infisical.example.com", universal, "").Descriptor
	if _, err := envsource.ReadCredentials(ctx, store, scope, descriptor); err != nil {
		t.Fatalf("ReadCredentials() borrowing from a project on ocel's own store = %v", err)
	}

	if _, err := envsource.Register(ctx, store, environment.TierProduction, infisicalRegistration("shared", "https://infisical.example.com", universal, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := envsource.ReadCredentials(ctx, store, scope, descriptor); err != nil {
		t.Fatalf("ReadCredentials() borrowing the credentials of a project on an env source = %v, want them its own", err)
	}

	if _, err := store.SetReference(ctx, scope, tierWide("", "INFISICAL_CLIENT_SECRET"), variablestore.Target{Project: "shared", Cell: variablestore.Cell{Key: "OTHER"}}); err != nil {
		t.Fatal(err)
	}
	_, err := envsource.ReadCredentials(ctx, store, scope, descriptor)
	if err == nil || !strings.Contains(err.Error(), "INFISICAL_CLIENT_SECRET") || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("ReadCredentials() borrowing a value an env source owns = %v, want it refused", err)
	}
}

func TestOnlyAnInfisicalEnvSourceHasACredential(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	if _, err := envsource.ReadCredentials(context.Background(), store, scope, execDescriptor(envsource.FormatJSON, "vault")); err == nil {
		t.Fatal("ReadCredentials() of an exec env source = nil, want a refusal")
	}
}

func TestProjectsReadingOneSourceWithOneCredentialShareADedupeKey(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	ctx := context.Background()
	setCredentials(t, store, "shared", "id", "secret")
	for _, project := range []string{"shop", "admin"} {
		for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"} {
			if _, err := store.SetReference(ctx, scopeOf(project), tierWide("", key), variablestore.Target{Project: "shared", Cell: variablestore.Cell{Key: key}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	setCredentials(t, store, "solo", "id", "secret")
	descriptor := infisicalRegistration("", "https://infisical.example.com", universal, "").Descriptor
	key := func(project string, descriptor envsource.Descriptor) string {
		t.Helper()
		key, err := envsource.DedupeKey(ctx, store, scopeOf(project), descriptor)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	shop := key("shop", descriptor)
	if admin := key("admin", descriptor); admin != shop {
		t.Errorf("admin and shop borrow one credential, and their keys differ: %q, %q", admin, shop)
	}
	if solo := key("solo", descriptor); solo == shop {
		t.Error("solo stores its own credential, and shares shop's key")
	}

	identity := infisicalRegistration("", "https://infisical.example.com", cloudIdentity, "").Descriptor
	if key("shop", identity) != key("admin", identity) {
		t.Error("two projects signing in as one cloud identity have different keys")
	}
	other := infisicalRegistration("", "https://infisical.example.com", identityAuth("identity-2"), "").Descriptor
	if key("shop", identity) == key("shop", other) {
		t.Error("two cloud identities share a key")
	}
}
