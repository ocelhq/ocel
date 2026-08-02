package cli

import (
	"reflect"
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

// TestAppVariables_CarriesClientAccessibilityFromTheDeclaration proves the
// flag that decides whether a value is inlined into a browser bundle survives
// the join. Nothing else knows it: resolution answers what a value is, only
// the declaration answers who may read it.
func TestAppVariables_CarriesClientAccessibilityFromTheDeclaration(t *testing.T) {
	definitions := []*resourcesv1.VariableDefinition{
		{Key: "PUBLIC_SITE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, ClientAccessible: true},
		{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
	}
	resolved := map[string]envgate.Resolved{
		"PUBLIC_SITE_URL": {Value: "https://example.com"},
		"INTERNAL_URL":    {Value: "http://internal"},
	}

	got := appVariables(definitions, resolved)
	if len(got) != 2 {
		t.Fatalf("appVariables = %+v, want both declarations", got)
	}
	if !got[0].ClientAccessible {
		t.Errorf("PUBLIC_SITE_URL = %+v, want it marked client-accessible", got[0])
	}
	if got[1].ClientAccessible {
		t.Errorf("INTERNAL_URL = %+v, want it left server-only", got[1])
	}
}

// TestBuildEnv_ExportsAClientValueUnderThePublicPrefixAndNoServerOnlyOne
// proves the one thing the framework's static replacement needs: a
// client-accessible value present under the framework's public prefix before
// the build runs, and nothing else there. A server-only value under that
// prefix would be inlined into the browser bundle by the framework itself.
func TestBuildEnv_ExportsAClientValueUnderThePublicPrefixAndNoServerOnlyOne(t *testing.T) {
	variables := map[string][]manifestbuilder.Variable{
		"storefront": {
			{Key: "PUBLIC_SITE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://example.com", ClientAccessible: true},
			{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "http://internal"},
			{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
		},
	}

	env := buildEnv(variables)["storefront"]
	if got, want := env["NEXT_PUBLIC_PUBLIC_SITE_URL"], "https://example.com"; got != want {
		t.Errorf("NEXT_PUBLIC_PUBLIC_SITE_URL = %q, want %q", got, want)
	}
	if got, want := env["PUBLIC_SITE_URL"], "https://example.com"; got != want {
		t.Errorf("PUBLIC_SITE_URL = %q, want %q: the server half of a client value is still delivered", got, want)
	}
	for _, name := range []string{"NEXT_PUBLIC_INTERNAL_URL", "NEXT_PUBLIC_STRIPE_API_KEY"} {
		if _, ok := env[name]; ok {
			t.Errorf("env carries %s; a server-only value under the public prefix is inlined into the browser bundle", name)
		}
	}
}

// TestBuildEnv_ExportsOnlyPlaintextValues proves what each app's build runs
// with: an encrypted-class value has no business in a build process's
// environment, whatever app it belongs to.
func TestBuildEnv_ExportsOnlyPlaintextValues(t *testing.T) {
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

	env := buildEnv(variables)
	if got, want := env["admin"]["SHARED_ID"], "same"; got != want {
		t.Errorf("admin SHARED_ID = %q, want %q", got, want)
	}
	if _, ok := env["admin"]["STRIPE_API_KEY"]; ok {
		t.Errorf("admin env = %v, must not carry an encrypted-class value", env["admin"])
	}
	if _, ok := env["storefront"]["STRIPE_API_KEY"]; ok {
		t.Errorf("storefront env = %v, must not carry an encrypted-class value", env["storefront"])
	}
}

// TestBuildEnv_GivesEachAppItsOwnValueForADivergedKey proves the case folders
// exist for reaches the build. Each app is built under its own environment, so
// a key two apps resolve differently is inlinable by both — it is not the
// build's job to pick one value or to leave the key unset.
func TestBuildEnv_GivesEachAppItsOwnValueForADivergedKey(t *testing.T) {
	variables := map[string][]manifestbuilder.Variable{
		"storefront": {{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-store"}},
		"admin":      {{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-admin"}},
	}

	env := buildEnv(variables)
	if got, want := env["storefront"]["POSTHOG_ID"], "ph-store"; got != want {
		t.Errorf("storefront POSTHOG_ID = %q, want %q", got, want)
	}
	if got, want := env["admin"]["POSTHOG_ID"], "ph-admin"; got != want {
		t.Errorf("admin POSTHOG_ID = %q, want %q", got, want)
	}
}

// TestBuildEnv_TheRootStandInIsKeyedByNoAppAtAll proves the project that
// configures no apps still hands its values to the build. Nothing yet knows
// the name of the app the builder will detect, so its environment travels
// under no name and the build exports it for whatever it finds.
func TestBuildEnv_TheRootStandInIsKeyedByNoAppAtAll(t *testing.T) {
	variables := map[string][]manifestbuilder.Variable{
		rootApp: {{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-123"}},
	}

	env := buildEnv(variables)
	if _, ok := env[rootApp]; ok {
		t.Errorf("env = %v, still keyed by a placeholder name no build knows", env)
	}
	if got, want := env[""]["POSTHOG_ID"], "ph-123"; got != want {
		t.Errorf("root env POSTHOG_ID = %q, want %q", got, want)
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
