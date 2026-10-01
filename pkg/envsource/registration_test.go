package envsource_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/keyvalue"
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

func recordUndecodable(t *testing.T, store keyvalue.Store, tier environment.Tier, project string) {
	t.Helper()
	ctx := context.Background()
	key := keyvalue.Partition{Tier: tier, Root: keyvalue.RootEnvSources}.Key(project)
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, key)
	if err != nil {
		t.Fatal(err)
	}
	registration := map[string]json.RawMessage{"project": json.RawMessage(`"` + project + `"`), "folders": json.RawMessage(`[""]`)}
	if len(recorded.Value) > 0 {
		if err := json.Unmarshal(recorded.Value, &registration); err != nil {
			t.Fatal(err)
		}
	}
	registration["descriptor"] = json.RawMessage(`{"kind":"infisical","infisical":{"project":"p-1","environment":"prod"}}`)
	if recorded.Value, err = json.Marshal(registration); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, recorded); err != nil {
		t.Fatal(err)
	}
}

func statusEntries(t *testing.T, store keyvalue.Store, tier environment.Tier) []keyvalue.Key {
	t.Helper()
	listed, err := store.List(context.Background(), keyvalue.Partition{Tier: tier, Root: keyvalue.RootEnvSourceStatus})
	if err != nil {
		t.Fatal(err)
	}
	var keys []keyvalue.Key
	for _, entry := range listed {
		keys = append(keys, entry.Key)
	}
	return keys
}

func TestARegistrationThatNoLongerDecodesIsReportedNamingItsProjectAndKind(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	recordUndecodable(t, store.KeyValues, environment.TierProduction, "shop")

	_, _, err := envsource.Registered(context.Background(), store.KeyValues, environment.TierProduction, "shop")
	var undecodable *envsource.UndecodableRegistrationError
	if !errors.As(err, &undecodable) || undecodable.Project != "shop" || undecodable.Kind != "infisical" {
		t.Fatalf("Registered() = %v, want an UndecodableRegistrationError naming shop and its infisical kind", err)
	}
}

func TestARegistrationThatNoLongerDecodesIsReplacedByTheNextOne(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	ctx := context.Background()
	tier := environment.TierProduction
	register(t, store, infisicalRegistration("shop", "https://old.example.com", universal, ""))
	recordUndecodable(t, store.KeyValues, tier, "shop")

	if _, err := envsource.Register(ctx, store, tier, infisicalRegistration("shop", "https://new.example.com", cloudIdentity, "")); err != nil {
		t.Fatalf("Register() over a registration that no longer decodes = %v, want it replaced", err)
	}
	current, registered, err := envsource.Registered(ctx, store.KeyValues, tier, "shop")
	if err != nil || !registered || !strings.Contains(string(current.Descriptor.Options()), "https://new.example.com") {
		t.Fatalf("Registered() = %+v, %v, %v, want the new registration", current, registered, err)
	}
	if sharers := statusEntries(t, store.KeyValues, tier); len(sharers) != 1 {
		t.Fatalf("status entries = %v, want only the new registration's sharer, the undecodable one's released", sharers)
	}
}

func TestForgettingARegistrationThatNoLongerDecodesReleasesWhatItShared(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	ctx := context.Background()
	tier := environment.TierProduction
	register(t, store, infisicalRegistration("shop", "https://old.example.com", universal, ""))
	recordUndecodable(t, store.KeyValues, tier, "shop")

	if err := envsource.ForgetProject(ctx, store, tier, "shop"); err != nil {
		t.Fatalf("ForgetProject() = %v", err)
	}
	if _, registered, err := envsource.Registered(ctx, store.KeyValues, tier, "shop"); err != nil || registered {
		t.Fatalf("Registered() after ForgetProject = %v, %v, want nothing", registered, err)
	}
	if left := statusEntries(t, store.KeyValues, tier); len(left) != 0 {
		t.Fatalf("status entries = %v, want none left", left)
	}
}

func TestRegistrationsSkipsOneThatNoLongerDecodesAndReportsIt(t *testing.T) {
	t.Parallel()
	store, _ := storeFixture()
	ctx := context.Background()
	tier := environment.TierProduction
	register(t, store, infisicalRegistration("admin", "https://infisical.example.com", cloudIdentity, ""))
	recordUndecodable(t, store.KeyValues, tier, "shop")

	all, err := envsource.Registrations(ctx, store.KeyValues, tier)
	var undecodable *envsource.UndecodableRegistrationError
	if !errors.As(err, &undecodable) || undecodable.Project != "shop" {
		t.Fatalf("Registrations() error = %v, want shop reported as undecodable", err)
	}
	if len(all) != 1 || all[0].Project != "admin" {
		t.Fatalf("Registrations() = %+v, want admin alone", all)
	}
}
