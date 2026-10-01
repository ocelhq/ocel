package envsource_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

var universal = &envsource.InfisicalAuth{Universal: &envsource.UniversalAuth{
	ClientID:     envsource.Variable{Name: "INFISICAL_CLIENT_ID"},
	ClientSecret: envsource.Variable{Name: "INFISICAL_CLIENT_SECRET"},
}}

var cloudIdentity = identityAuth("identity-1")

func identityAuth(id string) *envsource.InfisicalAuth {
	return &envsource.InfisicalAuth{Identity: &envsource.IdentityAuth{IdentityID: id}}
}

func infisicalDescriptor(options envsource.InfisicalOptions) envsource.Descriptor {
	encoded, err := json.Marshal(options)
	if err != nil {
		panic(err)
	}
	descriptor, err := envsource.NewDescriptor("infisical", encoded)
	if err != nil {
		panic(err)
	}
	return descriptor
}

func infisicalWriting(project, host string, auth *envsource.InfisicalAuth, write envsource.WritePolicy, folders ...string) envsource.Registration {
	return envsource.Registration{
		Project: project,
		Descriptor: infisicalDescriptor(envsource.InfisicalOptions{
			Project: "p-1", Environment: "prod", Path: "/", Host: host, Auth: auth, Write: write,
		}),
		Folders: folders,
	}
}

func infisicalRegistration(project, host string, auth *envsource.InfisicalAuth, folders ...string) envsource.Registration {
	return infisicalWriting(project, host, auth, envsource.WriteNever, folders...)
}

func TestARegistrationIsReadBackPerProjectWithItsFoldersSortedOnce(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	keyValues := store.KeyValues
	ctx := context.Background()
	for _, registration := range []envsource.Registration{
		infisicalRegistration("shop", "https://infisical.example.com", universal, "/web", "", "/web"),
		infisicalRegistration("admin", "https://infisical.example.com", cloudIdentity, ""),
	} {
		if _, err := envsource.Register(ctx, store, environment.TierProduction, registration); err != nil {
			t.Fatal(err)
		}
	}

	shop, registered, err := envsource.Registered(ctx, keyValues, environment.TierProduction, "shop")
	if err != nil || !registered {
		t.Fatalf("Registered() = %v, %v", registered, err)
	}
	if !slices.Equal(shop.Folders, []string{"", "/web"}) || shop.Descriptor.ID() != "infisical:p-1/prod" {
		t.Fatalf("Registered() = %+v, want the folders sorted once and the descriptor kept", shop)
	}
	if want := []variablestore.Cell{{Key: "INFISICAL_CLIENT_ID"}, {Key: "INFISICAL_CLIENT_SECRET"}}; !slices.Equal(shop.Credentials(), want) {
		t.Fatalf("Credentials() = %v, want %v", shop.Credentials(), want)
	}
	if _, registered, _ := envsource.Registered(ctx, keyValues, environment.TierPreview, "shop"); registered {
		t.Fatal("a production registration was read back in preview")
	}

	all, err := envsource.Registrations(ctx, keyValues, environment.TierProduction)
	if err != nil || len(all) != 2 || all[0].Project != "admin" || all[1].Project != "shop" {
		t.Fatalf("Registrations() = %+v, %v, want admin then shop", all, err)
	}

	if err := envsource.ForgetProject(ctx, store, environment.TierProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, registered, err := envsource.Registered(ctx, keyValues, environment.TierProduction, "shop"); err != nil || registered {
		t.Fatalf("Registered() after ForgetProject = %v, %v, want nothing", registered, err)
	}
}

func TestRestoringARegistrationLeavesOneAnotherDeployRegisteredSince(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	ctx := context.Background()
	tier := environment.TierProduction
	register := func(host string) envsource.Registration {
		t.Helper()
		registration, err := envsource.Register(ctx, store, tier, infisicalRegistration("shop", host, universal, ""))
		if err != nil {
			t.Fatal(err)
		}
		return registration
	}
	working := register("https://working.example.com")
	failed := register("https://failed.example.com")
	register("https://since.example.com")

	if err := envsource.RestoreRegistration(ctx, store, tier, failed, &working); err != nil {
		t.Fatal(err)
	}
	current, _, err := envsource.Registered(ctx, store.KeyValues, tier, "shop")
	if err != nil || !strings.Contains(string(current.Descriptor.Options()), "https://since.example.com") {
		t.Fatalf("Registered() = %+v, %v, want the registration made since the failed one left in place", current, err)
	}
}
