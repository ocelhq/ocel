package envsource_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func scopeOf(project string) envvars.Scope {
	return envvars.Scope{Project: project, Class: edge.ClassProduction}
}

func setCredentials(t *testing.T, store envvars.Store, project, clientID, clientSecret string) {
	t.Helper()
	for key, plaintext := range map[string]string{"INFISICAL_CLIENT_ID": clientID, "INFISICAL_CLIENT_SECRET": clientSecret} {
		if _, err := store.Set(context.Background(), scopeOf(project), classWide("", key), plaintext, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnUnsetCredentialIsRefusedWithTheCommandThatSetsIt(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	descriptor := infisicalRegistration("shop", "https://infisical.example.com", universal, "").Descriptor
	for class, flag := range map[edge.Class]string{edge.ClassProduction: "", edge.ClassPreview: " --preview"} {
		_, err := envsource.ReadCredential(context.Background(), store, envvars.Scope{Project: "shop", Class: class}, descriptor, envsource.Login{})
		var refused *envsource.CredentialError
		if !errors.As(err, &refused) || refused.Variable != "INFISICAL_CLIENT_ID" || !strings.Contains(err.Error(), "ocel env set INFISICAL_CLIENT_ID=<VALUE>"+flag+"`") {
			t.Errorf("ReadCredential() in %s = %v, want INFISICAL_CLIENT_ID refused with the command that sets it", class, err)
		}
	}
}

func TestACredentialAnEnvSourceWroteIsRefused(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	descriptor := infisicalRegistration("shop", "https://infisical.example.com", universal, "").Descriptor
	setCredentials(t, store, "shop", "id", "secret")
	if _, err := envsource.ReadCredential(ctx, store, scope, descriptor, envsource.Login{}); err != nil {
		t.Fatalf("ReadCredential() with both set = %v", err)
	}

	if _, err := store.SetFromEnvSource(ctx, scope, classWide("", "INFISICAL_CLIENT_SECRET"), "cached", envvars.Provenance{EnvSource: "infisical:p-1/prod", Version: "s9@1"}, 1); err != nil {
		t.Fatal(err)
	}
	_, err := envsource.ReadCredential(ctx, store, scope, descriptor, envsource.Login{})
	if err == nil || !strings.Contains(err.Error(), "INFISICAL_CLIENT_SECRET") || !strings.Contains(err.Error(), "infisical:p-1/prod") {
		t.Fatalf("ReadCredential() with a credential an env source wrote = %v, want it refused naming both", err)
	}
}

func TestACredentialReferencedFromAProjectMustBeOneThatProjectStoresItself(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	ctx := context.Background()
	shared := scopeOf("shared")
	for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET", "OTHER"} {
		if _, err := store.Set(ctx, shared, classWide("", key), "x", nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"} {
		if _, err := store.SetReference(ctx, scope, classWide("", key), envvars.Target{Project: "shared", Cell: envvars.Cell{Key: key}}); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := infisicalRegistration("shop", "https://infisical.example.com", universal, "").Descriptor
	if _, err := envsource.ReadCredential(ctx, store, scope, descriptor, envsource.Login{}); err != nil {
		t.Fatalf("ReadCredential() borrowing from a project on ocel's own store = %v", err)
	}

	if err := envsource.Register(ctx, store.Records, edge.ClassProduction, infisicalRegistration("shared", "https://infisical.example.com", universal, "")); err != nil {
		t.Fatal(err)
	}
	if _, err := envsource.ReadCredential(ctx, store, scope, descriptor, envsource.Login{}); err != nil {
		t.Fatalf("ReadCredential() borrowing the credentials of a project on an env source = %v, want them its own", err)
	}

	if _, err := store.SetReference(ctx, scope, classWide("", "INFISICAL_CLIENT_SECRET"), envvars.Target{Project: "shared", Cell: envvars.Cell{Key: "OTHER"}}); err != nil {
		t.Fatal(err)
	}
	_, err := envsource.ReadCredential(ctx, store, scope, descriptor, envsource.Login{})
	if err == nil || !strings.Contains(err.Error(), "INFISICAL_CLIENT_SECRET") || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("ReadCredential() borrowing a value an env source owns = %v, want it refused", err)
	}
}

func TestOnlyAnInfisicalEnvSourceHasACredential(t *testing.T) {
	t.Parallel()
	store, scope := storeFixture()
	if _, err := envsource.ReadCredential(context.Background(), store, scope, envsource.Descriptor{Kind: envsource.Exec, Exec: &envsource.ExecOptions{}}, envsource.Login{}); err == nil {
		t.Fatal("ReadCredential() of an exec env source = nil, want a refusal")
	}
}

func TestProjectsReadingOneSourceWithOneCredentialShareADedupeKey(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	ctx := context.Background()
	setCredentials(t, store, "shared", "id", "secret")
	for _, project := range []string{"shop", "admin"} {
		for _, key := range []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"} {
			if _, err := store.SetReference(ctx, scopeOf(project), classWide("", key), envvars.Target{Project: "shared", Cell: envvars.Cell{Key: key}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	setCredentials(t, store, "solo", "id", "secret")
	descriptor := infisicalRegistration("", "https://infisical.example.com", universal, "").Descriptor
	shop := envsource.DedupeKey(ctx, store, scopeOf("shop"), descriptor)
	if admin := envsource.DedupeKey(ctx, store, scopeOf("admin"), descriptor); admin != shop {
		t.Errorf("admin and shop borrow one credential, and their keys differ: %q, %q", admin, shop)
	}
	if solo := envsource.DedupeKey(ctx, store, scopeOf("solo"), descriptor); solo == shop {
		t.Error("solo stores its own credential, and shares shop's key")
	}

	identity := infisicalRegistration("", "https://infisical.example.com", cloudIdentity, "").Descriptor
	if envsource.DedupeKey(ctx, store, scopeOf("shop"), identity) != envsource.DedupeKey(ctx, store, scopeOf("admin"), identity) {
		t.Error("two projects signing in as one cloud identity have different keys")
	}
	other := infisicalRegistration("", "https://infisical.example.com", envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "identity-2"}, "").Descriptor
	if envsource.DedupeKey(ctx, store, scopeOf("shop"), identity) == envsource.DedupeKey(ctx, store, scopeOf("shop"), other) {
		t.Error("two cloud identities share a key")
	}
}
