package envsource_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
)

var universal = envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVariable: "INFISICAL_CLIENT_ID", ClientSecretVariable: "INFISICAL_CLIENT_SECRET"}

var cloudIdentity = envsource.InfisicalAuth{Method: envsource.AuthIdentity, IdentityID: "identity-1"}

func infisicalRegistration(project, host string, auth envsource.InfisicalAuth, folders ...string) envsource.Registration {
	return envsource.Registration{
		Project: project,
		Descriptor: envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
			Project: "p-1", Environment: "prod", Path: "/", Host: host, Auth: auth, Write: envsource.WriteNever,
		}},
		Folders: folders,
	}
}

func TestARegistrationIsReadBackPerProjectWithItsFoldersSortedOnce(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	records := store.Records
	ctx := context.Background()
	for _, registration := range []envsource.Registration{
		infisicalRegistration("shop", "https://infisical.example.com", universal, "/web", "", "/web"),
		infisicalRegistration("admin", "https://infisical.example.com", cloudIdentity, ""),
	} {
		if _, err := envsource.Register(ctx, store, edge.ClassProduction, registration); err != nil {
			t.Fatal(err)
		}
	}

	shop, registered, err := envsource.Registered(ctx, records, edge.ClassProduction, "shop")
	if err != nil || !registered {
		t.Fatalf("Registered() = %v, %v", registered, err)
	}
	if !slices.Equal(shop.Folders, []string{"", "/web"}) || shop.Descriptor.ID() != "infisical:p-1/prod" {
		t.Fatalf("Registered() = %+v, want the folders sorted once and the descriptor kept", shop)
	}
	if want := []envvars.Cell{{Key: "INFISICAL_CLIENT_ID"}, {Key: "INFISICAL_CLIENT_SECRET"}}; !slices.Equal(shop.Credentials(), want) {
		t.Fatalf("Credentials() = %v, want %v", shop.Credentials(), want)
	}
	if _, registered, _ := envsource.Registered(ctx, records, edge.ClassPreview, "shop"); registered {
		t.Fatal("a production registration was read back in preview")
	}

	all, err := envsource.Registrations(ctx, records, edge.ClassProduction)
	if err != nil || len(all) != 2 || all[0].Project != "admin" || all[1].Project != "shop" {
		t.Fatalf("Registrations() = %+v, %v, want admin then shop", all, err)
	}

	if err := envsource.ForgetProject(ctx, store, edge.ClassProduction, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, registered, err := envsource.Registered(ctx, records, edge.ClassProduction, "shop"); err != nil || registered {
		t.Fatalf("Registered() after ForgetProject = %v, %v, want nothing", registered, err)
	}
}

func TestRestoringARegistrationLeavesOneAnotherDeployRegisteredSince(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	ctx := context.Background()
	class := edge.ClassProduction
	register := func(host string) envsource.Registration {
		t.Helper()
		registration, err := envsource.Register(ctx, store, class, infisicalRegistration("shop", host, universal, ""))
		if err != nil {
			t.Fatal(err)
		}
		return registration
	}
	working := register("https://working.example.com")
	failed := register("https://failed.example.com")
	register("https://since.example.com")

	if err := envsource.RestoreRegistration(ctx, store, class, failed, &working); err != nil {
		t.Fatal(err)
	}
	current, _, err := envsource.Registered(ctx, store.Records, class, "shop")
	if err != nil || current.Descriptor.Infisical.Host != "https://since.example.com" {
		t.Fatalf("Registered() = %+v, %v, want the registration made since the failed one left in place", current, err)
	}
}
