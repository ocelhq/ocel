package cli

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func definition(key string, class resourcesv1.VariableClass) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{Key: key, Class: class}
}

func TestAppVariables(t *testing.T) {
	t.Parallel()

	t.Run("pairs each declaration with what was resolved for it", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("omits a key this app cannot read", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("CHECKOUT_ONLY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		}

		if got := appVariables(definitions, map[string]envgate.Resolved{}); len(got) != 0 {
			t.Fatalf("appVariables = %+v, want nothing for a key this app resolves no cell for", got)
		}
	})

	t.Run("carries client accessibility from the declaration", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("carries the version each value resolved at", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("PLAIN_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			definition("LIVE_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		}
		resolved := map[string]envgate.Resolved{
			"PLAIN_KEY": {Value: "v", Version: 2},
			"LIVE_KEY":  {Version: 9},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if got[0].Key != "PLAIN_KEY" || got[0].Version != 2 {
			t.Errorf("PLAIN_KEY = %+v, want the version its cell resolved at", got[0])
		}
		if got[1].Key != "LIVE_KEY" || got[1].Version != 9 {
			t.Errorf("LIVE_KEY = %+v, want its cell's version carried too", got[1])
		}
	})

	t.Run("carries the folder each key resolved from", func(t *testing.T) {
		t.Parallel()

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
	})
}

func TestBuildEnv(t *testing.T) {
	t.Parallel()

	t.Run("exports every plaintext value under its own name and nothing else", func(t *testing.T) {
		t.Parallel()

		plans := []appPlan{{name: "storefront", variables: []manifestbuilder.Variable{
			{Key: "NEXT_PUBLIC_SITE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://example.com", ClientAccessible: true},
			{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "http://internal"},
			{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
		}}}

		env := buildEnv(plans)["storefront"]
		if got, want := env["NEXT_PUBLIC_SITE_URL"], "https://example.com"; got != want {
			t.Errorf("NEXT_PUBLIC_SITE_URL = %q, want %q", got, want)
		}
		if got, want := env["INTERNAL_URL"], "http://internal"; got != want {
			t.Errorf("INTERNAL_URL = %q, want %q: a build reads the plaintext class, client-accessible or not", got, want)
		}
		if _, ok := env["STRIPE_API_KEY"]; ok {
			t.Error("env carries STRIPE_API_KEY; an encrypted class is nothing a build may read")
		}
		if _, ok := env["NEXT_PUBLIC_NEXT_PUBLIC_SITE_URL"]; ok {
			t.Error("env carries a prefixed name; a key is delivered as it was declared")
		}
	})

	t.Run("exports only plaintext values", func(t *testing.T) {
		t.Parallel()

		plans := []appPlan{
			{name: "admin", variables: []manifestbuilder.Variable{
				{Key: "SHARED_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "same"},
				{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-admin"},
				{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
			}},
			{name: "storefront", variables: []manifestbuilder.Variable{
				{Key: "SHARED_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "same"},
				{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-store"},
				{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
			}},
		}

		env := buildEnv(plans)
		if got, want := env["admin"]["SHARED_ID"], "same"; got != want {
			t.Errorf("admin SHARED_ID = %q, want %q", got, want)
		}
		if _, ok := env["admin"]["STRIPE_API_KEY"]; ok {
			t.Errorf("admin env = %v, must not carry an encrypted-class value", env["admin"])
		}
		if _, ok := env["storefront"]["STRIPE_API_KEY"]; ok {
			t.Errorf("storefront env = %v, must not carry an encrypted-class value", env["storefront"])
		}
	})

	t.Run("gives each app its own value for a diverged key", func(t *testing.T) {
		t.Parallel()

		plans := []appPlan{
			{name: "storefront", variables: []manifestbuilder.Variable{{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-store"}}},
			{name: "admin", variables: []manifestbuilder.Variable{{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-admin"}}},
		}

		env := buildEnv(plans)
		if got, want := env["storefront"]["POSTHOG_ID"], "ph-store"; got != want {
			t.Errorf("storefront POSTHOG_ID = %q, want %q", got, want)
		}
		if got, want := env["admin"]["POSTHOG_ID"], "ph-admin"; got != want {
			t.Errorf("admin POSTHOG_ID = %q, want %q", got, want)
		}
	})

	t.Run("the root stand-in is keyed by no app at all", func(t *testing.T) {
		t.Parallel()

		variables := map[string][]manifestbuilder.Variable{
			rootApp: {{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-123"}},
		}

		env := buildEnv(appPlans(&projectconfig.Config{Dir: t.TempDir()}, variables))
		if _, ok := env[rootApp]; ok {
			t.Errorf("env = %v, still keyed by a placeholder name no build knows", env)
		}
		if got, want := env[""]["POSTHOG_ID"], "ph-123"; got != want {
			t.Errorf("root env POSTHOG_ID = %q, want %q", got, want)
		}
	})
}

func TestVariablesByApp(t *testing.T) {
	t.Parallel()

	t.Run("root resolution reaches the app nothing configured", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("configured apps keep their own resolution", func(t *testing.T) {
		t.Parallel()

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
	})
}

func TestToApps(t *testing.T) {
	t.Parallel()

	t.Run("carries the folder binding into the manifest", func(t *testing.T) {
		t.Parallel()

		got := toApps([]projectconfig.App{
			{Name: "admin", Framework: "express", Folder: "/admin"},
			{Name: "web", Framework: "express"},
		}, nil)

		want := []manifestbuilder.App{
			{Name: "admin", Framework: "express", Folder: "/admin"},
			{Name: "web", Framework: "express"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("toApps() = %+v, want %+v", got, want)
		}
	})

	t.Run("hands each app only the usage edges attributed to it", func(t *testing.T) {
		t.Parallel()

		got := toApps([]projectconfig.App{{Name: "admin"}, {Name: "web"}}, []attribution.Usage{
			{App: "web", Type: naming.TokenPostgres, ID: "main", Files: []string{"apps/web/src/server.ts"}},
			{App: "admin", Type: naming.TokenBucket, ID: "uploads", Files: []string{"apps/admin/src/upload.ts"}},
		})

		want := []manifestbuilder.App{
			{Name: "admin", Usages: []manifestbuilder.Usage{{Type: naming.TokenBucket, ID: "uploads", Files: []string{"apps/admin/src/upload.ts"}}}},
			{Name: "web", Usages: []manifestbuilder.Usage{{Type: naming.TokenPostgres, ID: "main", Files: []string{"apps/web/src/server.ts"}}}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("toApps() = %+v, want %+v", got, want)
		}
	})
}
