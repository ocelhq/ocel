package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func definition(key string, class resourcesv1.VariableClass) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{Key: key, Class: class}
}

// TestAppVariables_PairsEachDeclarationWithWhatWasResolvedForIt proves the two
// halves the deploy needs arrive joined: the class from the declaration decides
// delivery, the resolution decides the value, and a key nothing resolved is
// carried with no value rather than dropped — its class still says where it
// comes from.
func TestAppVariables_PairsEachDeclarationWithWhatWasResolvedForIt(t *testing.T) {
	definitions := []*resourcesv1.VariableDefinition{
		definition("POSTHOG_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		definition("WEBHOOK_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
	}
	resolved := map[string]envgate.Resolved{
		"POSTHOG_ID":     {Value: "ph-123"},
		"WEBHOOK_SECRET": {},
	}

	got := appVariables(definitions, resolved)
	if len(got) != 2 {
		t.Fatalf("appVariables = %+v, want both declarations", got)
	}
	if got[0].Key != "POSTHOG_ID" || got[0].Value != "ph-123" ||
		got[0].Class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
		t.Errorf("POSTHOG_ID = %+v, want its class and its resolved value", got[0])
	}
	if got[1].Value != "" {
		t.Errorf("WEBHOOK_SECRET = %+v, want no value: a live value never reaches a build host", got[1])
	}
}

// TestAppVariables_OmitsAKeyThisAppCannotRead proves an out-of-scope key is
// absent from what the app is deployed with, rather than delivered empty: the
// SDK's own named error is the whole remedy, and an empty string would defeat
// it.
func TestAppVariables_OmitsAKeyThisAppCannotRead(t *testing.T) {
	definitions := []*resourcesv1.VariableDefinition{
		definition("CHECKOUT_ONLY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
	}

	if got := appVariables(definitions, map[string]envgate.Resolved{}); len(got) != 0 {
		t.Fatalf("appVariables = %+v, want nothing for a key this app resolves no cell for", got)
	}
}

// TestBuildEnv_ExportsOnlyPlaintextValuesEveryAppAgreesOn proves what the app
// build runs with. One build process serves every app, so a value two apps
// resolve differently cannot be expressed there at all; and an encrypted-class
// value has no business in a build process's environment.
func TestBuildEnv_ExportsOnlyPlaintextValuesEveryAppAgreesOn(t *testing.T) {
	variables := map[string][]manifestbuilder.Variable{
		"admin": {
			{Key: "SHARED_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "same"},
			{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-admin"},
			{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
		},
		"storefront": {
			{Key: "SHARED_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "same"},
			{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-store"},
			{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
		},
	}

	env, _ := buildEnv(variables)
	if got, want := env["SHARED_ID"], "same"; got != want {
		t.Errorf("SHARED_ID = %q, want %q", got, want)
	}
	if _, ok := env["POSTHOG_ID"]; ok {
		t.Errorf("env = %v, must not give one app's value to a build that serves both", env)
	}
	if _, ok := env["STRIPE_API_KEY"]; ok {
		t.Errorf("env = %v, must not carry an encrypted-class value", env)
	}
}

// TestBuildEnv_ADivergedKeyIsWarnedAboutRatherThanQuietlyDropped proves the
// key a shared build cannot express is reported. Dropping it silently moves the
// failure into the built artifact — a framework that inlines the key emits an
// unset one — and nothing else on this path fails that far downstream.
func TestBuildEnv_ADivergedKeyIsWarnedAboutRatherThanQuietlyDropped(t *testing.T) {
	variables := map[string][]manifestbuilder.Variable{
		"storefront": {{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-store"}},
		"admin":      {{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-admin"}},
	}

	_, warnings := buildEnv(variables)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one, naming the key a shared build cannot export", warnings)
	}
	for _, want := range []string{"POSTHOG_ID", "admin", "storefront"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning = %q, want it to name %q", warnings[0], want)
		}
	}
}

// TestBuildEnv_AKeyEveryAppAgreesOnIsNotWarnedAbout is the counterweight: the
// warning must describe divergence, not merely the presence of two apps.
func TestBuildEnv_AKeyEveryAppAgreesOnIsNotWarnedAbout(t *testing.T) {
	variables := map[string][]manifestbuilder.Variable{
		"storefront": {{Key: "SHARED_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "same"}},
		"admin":      {{Key: "SHARED_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "same"}},
	}

	if _, warnings := buildEnv(variables); len(warnings) != 0 {
		t.Errorf("warnings = %q, want none for a value every app resolves the same", warnings)
	}
}

// TestVariablesByApp_RootResolutionReachesTheAppNothingConfigured proves the
// single-app project — which configures no apps at all, and is where a variable
// is most likely to be the only thing the app needs — still gets its values:
// the app the builder detected binds no folder, so what the root resolved is
// exactly what it reads.
func TestVariablesByApp_RootResolutionReachesTheAppNothingConfigured(t *testing.T) {
	root := []manifestbuilder.Variable{
		{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-123"},
	}
	functions := []manifestbuilder.Function{{Name: "index", App: "storefront"}}

	got := variablesByApp(map[string][]manifestbuilder.Variable{rootApp: root}, functions)
	if len(got[rootApp]) != 0 {
		t.Errorf("variables are still keyed by the placeholder root name: %v", got)
	}
	if len(got["storefront"]) != 1 || got["storefront"][0].Value != "ph-123" {
		t.Fatalf("storefront = %v, want the root resolution", got["storefront"])
	}
}

// TestVariablesByApp_ConfiguredAppsKeepTheirOwnResolution proves the per-app
// keying is left alone once apps are configured: that is the case folders exist
// for, and rekeying it would collapse the divergence.
func TestVariablesByApp_ConfiguredAppsKeepTheirOwnResolution(t *testing.T) {
	resolved := map[string][]manifestbuilder.Variable{
		"admin":      {{Key: "POSTHOG_ID", Value: "ph-admin"}},
		"storefront": {{Key: "POSTHOG_ID", Value: "ph-store"}},
	}
	functions := []manifestbuilder.Function{{Name: "index", App: "storefront"}}

	got := variablesByApp(resolved, functions)
	if len(got["admin"]) != 1 || got["admin"][0].Value != "ph-admin" {
		t.Fatalf("admin = %v, want its own resolution", got["admin"])
	}
	if got["storefront"][0].Value != "ph-store" {
		t.Fatalf("storefront = %v, want its own resolution", got["storefront"])
	}
}

// TestToApps_CarriesTheFolderBindingIntoTheManifest proves the binding
// declared in ocel.config.ts reaches the manifest builder. It is what the
// deployed app is told it is bound to, so a key scoped to another folder can
// fail naming both sides rather than reporting the root for every app.
func TestToApps_CarriesTheFolderBindingIntoTheManifest(t *testing.T) {
	got := toApps([]projectconfig.App{
		{Name: "admin", Framework: "express", Folder: "/admin"},
		{Name: "web", Framework: "express"},
	})

	want := []manifestbuilder.App{
		{Name: "admin", Framework: "express", Folder: "/admin"},
		{Name: "web", Framework: "express"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("toApps() = %+v, want %+v", got, want)
	}
}

// TestAppVariables_CarriesTheFolderEachKeyResolvedFrom proves the folder
// resolution computed survives the join. A live-class value carries no
// plaintext, so its folder is the only thing that makes it addressable at
// runtime: dropped here, the manifest names a key the store cannot be asked
// for. The two keys resolve from different folders for the same app, which is
// why the app's own binding cannot stand in for either.
func TestAppVariables_CarriesTheFolderEachKeyResolvedFrom(t *testing.T) {
	definitions := []*resourcesv1.VariableDefinition{
		definition("ROOT_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		definition("SCOPED_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
	}
	resolved := map[string]envgate.Resolved{
		"ROOT_KEY":   {},
		"SCOPED_KEY": {Folder: "/admin"},
	}

	got := appVariables(definitions, resolved)
	if len(got) != 2 {
		t.Fatalf("appVariables = %+v, want both declarations", got)
	}
	if got[0].Key != "ROOT_KEY" || got[0].Folder != "" {
		t.Errorf("ROOT_KEY = %+v, want the empty root spelling, never the store's %q sentinel", got[0], "/")
	}
	if got[1].Key != "SCOPED_KEY" || got[1].Folder != "/admin" {
		t.Errorf("SCOPED_KEY = %+v, want the folder it resolved from", got[1])
	}
}
