package envgate_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

var infisicalEnvSource = envgate.EnvSource{
	ID:          "infisical:p-1/prod",
	URLs:        map[string]string{"": "https://infisical.example/root", "/web": "https://infisical.example/web"},
	Credentials: []string{"INFISICAL_CLIENT_ID", "INFISICAL_CLIENT_SECRET"},
}

func TestARefusalOverValuesAnEnvSourceOwnsSaysToChangeThemThere(t *testing.T) {
	t.Parallel()

	t.Run("a missing value is set in the env source, linked by folder", func(t *testing.T) {
		t.Parallel()
		refusal := &envgate.Refusal{
			Problems: []*resourcesv1.VariableProblem{missing("STRIPE_KEY", "/web"), missing("DATABASE_URL", "")},
			Scope:    envgate.Scope{EnvSource: infisicalEnvSource},
		}
		out := refusal.Error()
		for _, want := range []string{"set them in infisical:p-1/prod", "root https://infisical.example/root", "/web https://infisical.example/web", "then deploy again"} {
			if !strings.Contains(out, want) {
				t.Errorf("refusal = %q, want %q", out, want)
			}
		}
		if strings.Contains(out, "ocel env set") {
			t.Errorf("refusal = %q, want no `ocel env set` offered for a value the env source owns", out)
		}
	})

	t.Run("an invalid value is fixed there, and only its folder is linked", func(t *testing.T) {
		t.Parallel()
		refusal := &envgate.Refusal{
			Problems: []*resourcesv1.VariableProblem{invalid("API_URL", "/web", "must be a URL")},
			Scope:    envgate.Scope{EnvSource: infisicalEnvSource},
		}
		out := refusal.Error()
		for _, want := range []string{"must be a URL", "fix it in infisical:p-1/prod", "https://infisical.example/web"} {
			if !strings.Contains(out, want) {
				t.Errorf("refusal = %q, want %q", out, want)
			}
		}
		if strings.Contains(out, "https://infisical.example/root") {
			t.Errorf("refusal = %q, want only the folder with the invalid value linked", out)
		}
	})

	t.Run("missing and invalid values together are set or fixed there", func(t *testing.T) {
		t.Parallel()
		refusal := &envgate.Refusal{
			Problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", ""), invalid("API_URL", "/web", "must be a URL")},
			Scope:    envgate.Scope{EnvSource: infisicalEnvSource},
		}
		if out := refusal.Error(); !strings.Contains(out, "set or fix them in infisical:p-1/prod") {
			t.Errorf("refusal = %q, want both kinds named in the remedy", out)
		}
	})

	t.Run("a named preview environment may still set its own value", func(t *testing.T) {
		t.Parallel()
		refusal := &envgate.Refusal{
			Problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", "")},
			Scope:    envgate.Scope{Preview: true, Environment: "pr-12", EnvSource: infisicalEnvSource},
		}
		if out := refusal.Error(); !strings.Contains(out, "ocel env set DATABASE_URL=<VALUE> --preview --environment pr-12") {
			t.Errorf("refusal = %q, want pr-12's own override offered", out)
		}
	})

	t.Run("in the browser the variables editor is still offered", func(t *testing.T) {
		t.Parallel()
		refusal := &envgate.Refusal{
			Problems: []*resourcesv1.VariableProblem{missing("STRIPE_KEY", "")},
			Scope:    envgate.Scope{EnvSource: infisicalEnvSource, Browser: true},
		}
		if out := refusal.Error(); !strings.Contains(out, "ocel env ui") {
			t.Errorf("refusal = %q, want the variables editor offered", out)
		}
	})

	t.Run("a tier ocel stores itself keeps offering ocel env set", func(t *testing.T) {
		t.Parallel()
		refusal := &envgate.Refusal{
			Problems: []*resourcesv1.VariableProblem{missing("STRIPE_KEY", "")},
			Scope:    envgate.Scope{EnvSource: envgate.EnvSource{ID: "builtin"}},
		}
		if out := refusal.Error(); !strings.Contains(out, "ocel env set STRIPE_KEY=<VALUE>") {
			t.Errorf("refusal = %q, want ocel env set offered", out)
		}
	})
}

func TestUndeclaredNamesWhatAnEnvSourceHasAndNothingDeclares(t *testing.T) {
	t.Parallel()
	source := infisicalEnvSource
	source.Present = []envgate.Cell{
		{Key: "DATABASE_URL"},
		{Key: "STRIPE_KEY", Folder: "/web"},
		{Key: "OLD_TOKEN", Folder: "/web"},
		{Key: "SCOPED", Folder: "/api"},
		{Key: "ORDERS_PASSWORD"},
		{Key: "INFISICAL_CLIENT_ID"},
	}
	g := prefetched(t, newFakeValues(), envgate.Scope{EnvSource: source, Bindings: []envgate.BindingVariables{ordersBinding}})
	declare(t, g,
		def("DATABASE_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		def("STRIPE_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		scoped("SCOPED", "/web"),
	)

	warnings := envgate.Undeclared(g.Declared(), g.Scope().EnvSource)
	if len(warnings) != 2 {
		t.Fatalf("Undeclared = %q, want OLD_TOKEN and SCOPED in the folder nothing declares it for", warnings)
	}
	for i, want := range []string{"OLD_TOKEN", "SCOPED"} {
		if !strings.Contains(warnings[i], want) || !strings.Contains(warnings[i], "infisical:p-1/prod") {
			t.Errorf("warning %d = %q, want %s named with its env source", i, warnings[i], want)
		}
	}
}

func TestUndeclaredIsEmptyForATierOcelStoresItself(t *testing.T) {
	t.Parallel()
	source := envgate.EnvSource{ID: "builtin", Present: []envgate.Cell{{Key: "OLD_TOKEN"}}}
	if warnings := envgate.Undeclared(nil, source); len(warnings) != 0 {
		t.Errorf("Undeclared = %q, want none", warnings)
	}
}

func TestTheCredentialsAnEnvSourceLogsInWithAreImpliedDeclarations(t *testing.T) {
	t.Parallel()

	t.Run("they are declared as a secret group apart from the app's own", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{EnvSource: infisicalEnvSource})
		declare(t, g, def("LOG_LEVEL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))

		for _, definition := range g.Definitions() {
			if slices.Contains(infisicalEnvSource.Credentials, definition.GetKey()) {
				t.Errorf("Definitions() contains %s, which would deliver it to the app", definition.GetKey())
			}
		}
		for _, key := range infisicalEnvSource.Credentials {
			i := slices.IndexFunc(g.Declared(), func(d *resourcesv1.VariableDefinition) bool { return d.GetKey() == key })
			if i < 0 {
				t.Fatalf("Declared() lacks %s", key)
			}
			definition := g.Declared()[i]
			if definition.GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_SECRET || definition.GetGroup() != envgate.EnvSourceGroup || definition.GetSource() != "envSource.production" {
				t.Errorf("%s declared as %+v, want a secret in the %s group declared by envSource.production", key, definition, envgate.EnvSourceGroup)
			}
		}
		definitions, groups := envgate.Declarations(g.Scope())
		if !slices.ContainsFunc(definitions, func(d *resourcesv1.VariableDefinition) bool { return d.GetKey() == "INFISICAL_CLIENT_SECRET" }) ||
			!slices.ContainsFunc(groups, func(group *resourcesv1.GroupDefinition) bool { return group.GetKey() == envgate.EnvSourceGroup }) {
			t.Errorf("Declarations = %+v, %+v, want the credentials and their group", definitions, groups)
		}
	})

	t.Run("a deploy never refuses over them, since the env source's own sync needs them first", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{EnvSource: infisicalEnvSource})
		if err := g.Check(); err != nil {
			t.Errorf("Check = %v, want nothing missing", err)
		}
	})

	t.Run("an app declaring one is refused, naming the env source", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{EnvSource: infisicalEnvSource})
		_, err := g.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{{
			Key: "INFISICAL_CLIENT_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Source: "resources/env.ts",
		}}})
		var refusal *envgate.Refusal
		if err == nil || errors.As(err, &refusal) {
			t.Fatalf("DeclareEnv = %v, want the collision refused", err)
		}
		for _, want := range []string{"INFISICAL_CLIENT_SECRET", "resources/env.ts", "infisical:p-1/prod"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("DeclareEnv = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("an app declaring one as sensitive is refused before its value is read", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("INFISICAL_CLIENT_SECRET", "", "machine-identity-secret")
		g := prefetched(t, values, envgate.Scope{EnvSource: infisicalEnvSource})
		resp, err := g.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{
			def("INFISICAL_CLIENT_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE),
		}})
		if err == nil {
			t.Fatal("DeclareEnv = nil, want the collision refused")
		}
		if strings.Contains(resp.String(), "machine-identity-secret") {
			t.Errorf("DeclareEnv = %v, revealed the env source's credential", resp)
		}
	})

	t.Run("one is set at the project root alone", func(t *testing.T) {
		t.Parallel()
		scope := envgate.Scope{EnvSource: infisicalEnvSource}
		if err := envgate.CheckImpliedWritable(scope, "INFISICAL_CLIENT_ID", ""); err != nil {
			t.Errorf("CheckImpliedWritable at the root = %v, want nil", err)
		}
		err := envgate.CheckImpliedWritable(scope, "INFISICAL_CLIENT_ID", "/web")
		if err == nil || !strings.Contains(err.Error(), "infisical:p-1/prod") || !strings.Contains(err.Error(), "without --folder") {
			t.Errorf("CheckImpliedWritable in /web = %v, want it refused, naming the env source", err)
		}
	})
}

func TestTheMatrixNamesTheEnvSourceEachValueWasCopiedFrom(t *testing.T) {
	t.Parallel()
	values := newFakeValues()
	values.copyFrom("infisical:p-1/prod", "DATABASE_URL", "", "postgres://db")
	values.set("STRIPE_KEY", "", "sk")
	g := prefetched(t, values, envgate.Scope{EnvSource: infisicalEnvSource})
	declare(t, g, def("DATABASE_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN), def("STRIPE_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

	m := g.Matrix(nil)
	if got := cell(t, row(t, m, "DATABASE_URL"), "").EnvSource; got != "infisical:p-1/prod" {
		t.Errorf("DATABASE_URL env source = %q, want infisical:p-1/prod", got)
	}
	if got := cell(t, row(t, m, "STRIPE_KEY"), "").EnvSource; got != "" {
		t.Errorf("STRIPE_KEY env source = %q, want none: ocel stores it itself", got)
	}
}

func TestTheMatrixListsWhatAnEnvSourceCopiedAndNothingDeclares(t *testing.T) {
	t.Parallel()
	values := newFakeValues()
	values.copyFrom("infisical:p-1/prod", "DATABASE_URL", "", "postgres://db")
	values.copyFrom("infisical:p-1/prod", "OLD_TOKEN", "/web", "old")
	values.copyFrom("infisical:p-1/prod", "SCOPED", "/api", "x")
	values.copyFrom("infisical:p-1/prod", "ORDERS_PASSWORD", "", "pw")
	values.copyFrom("infisical:p-1/prod", "INFISICAL_CLIENT_ID", "", "id")
	values.set("LEFTOVER", "", "stored by ocel")
	g := prefetched(t, values, envgate.Scope{EnvSource: infisicalEnvSource, Bindings: []envgate.BindingVariables{ordersBinding}})
	declare(t, g, def("DATABASE_URL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN), scoped("SCOPED", "/web"))

	want := []envgate.UndeclaredCell{
		{Cell: envgate.Cell{Key: "OLD_TOKEN", Folder: "/web"}, EnvSource: "infisical:p-1/prod"},
		{Cell: envgate.Cell{Key: "SCOPED", Folder: "/api"}, EnvSource: "infisical:p-1/prod"},
	}
	if got := g.Matrix(nil).Undeclared; !slices.Equal(got, want) {
		t.Errorf("undeclared = %+v, want %+v", got, want)
	}
}

func TestAMatrixWithNothingUndeclaredEncodesNoUndeclaredList(t *testing.T) {
	t.Parallel()
	doc, err := json.Marshal(prefetched(t, newFakeValues()).Matrix(nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), "undeclared") {
		t.Errorf("matrix = %s, want no undeclared key", doc)
	}
}
