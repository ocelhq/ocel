package variables_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

var ordersBinding = variables.BindingVariables{
	Group: "postgres.orders",
	Site:  "bindings.postgres.orders",
	Keys:  []string{"ORDERS_HOST", "ORDERS_PASSWORD"},
}

func TestTheVariablesABindingReadsAreDeclaredForIt(t *testing.T) {
	t.Parallel()

	t.Run("a variable a binding reads and nobody set refuses the deploy with the command that sets it", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("ORDERS_HOST", "", "db.example.com")
		g := prefetched(t, values, variables.Scope{Tier: environmentv1.Tier_TIER_PREVIEW, Environment: "pr-12", Bindings: []variables.BindingVariables{ordersBinding}})

		err := g.RefuseIncomplete()
		var refusal *variables.MissingError
		if !errors.As(err, &refusal) {
			t.Fatalf("RefuseIncomplete() = %v, want a refusal", err)
		}
		message := err.Error()
		for _, want := range []string{"ORDERS_PASSWORD", "postgres.orders", "ocel env set ORDERS_PASSWORD=<VALUE> --preview --environment pr-12"} {
			if !strings.Contains(message, want) {
				t.Errorf("refusal = %q, want it to contain %q", message, want)
			}
		}
		if strings.Contains(message, "ORDERS_HOST  ") {
			t.Errorf("refusal = %q, names ORDERS_HOST, which is set", message)
		}
	})

	t.Run("a value set for the deploy's own environment satisfies it", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("ORDERS_HOST", "", "db.example.com")
		values.override("ORDERS_PASSWORD", "", "pr-12", "hunter2")
		g := prefetched(t, values, variables.Scope{Tier: environmentv1.Tier_TIER_PREVIEW, Environment: "pr-12", Bindings: []variables.BindingVariables{ordersBinding}})

		if err := g.RefuseIncomplete(); err != nil {
			t.Fatalf("RefuseIncomplete() = %v, want the override to satisfy the binding", err)
		}
		resolved, err := g.ResolveBindingVariables(context.Background())
		if err != nil {
			t.Fatalf("ResolveBindingVariables: %v", err)
		}
		if resolved["ORDERS_PASSWORD"] != "hunter2" || resolved["ORDERS_HOST"] != "db.example.com" {
			t.Errorf("ResolveBindingVariables = %v, want each variable's value for pr-12", resolved)
		}
	})

	t.Run("a value in a folder does not satisfy it, since a binding serves the whole project", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("ORDERS_HOST", "/web", "db.example.com")
		values.set("ORDERS_PASSWORD", "", "hunter2")
		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "web", Folder: "/web"}}, Bindings: []variables.BindingVariables{ordersBinding}})

		err := g.RefuseIncomplete()
		if err == nil || !strings.Contains(err.Error(), "ORDERS_HOST") {
			t.Fatalf("RefuseIncomplete() = %v, want ORDERS_HOST missing at the root", err)
		}
		if strings.Contains(err.Error(), "--folder") {
			t.Errorf("refusal = %q, want the root-level command", err)
		}
	})

	t.Run("the app is never handed a variable a binding reads", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("ORDERS_HOST", "", "db.example.com")
		values.set("ORDERS_PASSWORD", "", "hunter2")
		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "api"}}, Bindings: []variables.BindingVariables{ordersBinding}})
		declare(t, g, def("LOG_LEVEL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN))
		values.set("LOG_LEVEL", "", "debug")

		for _, definition := range g.Definitions() {
			if definition.GetKey() == "ORDERS_PASSWORD" || definition.GetKey() == "ORDERS_HOST" {
				t.Errorf("Definitions() contains %s, which would deliver it to the app", definition.GetKey())
			}
		}
		resolved, err := g.Resolve(context.Background(), "api")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if _, leaked := resolved["ORDERS_PASSWORD"]; leaked {
			t.Errorf("Resolve(api) = %v, want the binding's password kept from the app", resolved)
		}
	})

	t.Run("a key the app declares and a binding reads is refused, naming both", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("ORDERS_HOST", "", "db.example.com")
		values.set("ORDERS_PASSWORD", "", "hunter2")
		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "api"}}, Bindings: []variables.BindingVariables{ordersBinding}})
		_, err := g.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{{
			Key: "ORDERS_PASSWORD", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true, Source: "resources/env.ts",
		}}})
		if err == nil {
			t.Fatal("DeclareEnv = nil, want the collision refused")
		}
		var refusal *variables.MissingError
		if errors.As(err, &refusal) {
			t.Errorf("DeclareEnv = %v, a collision is no missing value the vars editor can fill", err)
		}
		for _, want := range []string{"ORDERS_PASSWORD", "resources/env.ts", "bindings.postgres.orders"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("DeclareEnv = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("a key a binding reads declared as plain is refused without its value reaching the app", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("ORDERS_HOST", "", "db.example.com")
		values.set("ORDERS_PASSWORD", "", "hunter2")
		g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "api"}}, Bindings: []variables.BindingVariables{ordersBinding}})
		resp, err := g.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{
			def("ORDERS_PASSWORD", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		}})
		if err == nil {
			t.Fatal("DeclareEnv = nil, want the collision refused")
		}
		if strings.Contains(resp.String(), "hunter2") || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("DeclareEnv = %v, %v, revealed the binding's password", resp, err)
		}
		if slices.ContainsFunc(g.Definitions(), func(d *resourcesv1.VariableDefinition) bool { return d.GetKey() == "ORDERS_PASSWORD" }) {
			t.Errorf("Definitions() = %v, kept the refused declaration", g.Definitions())
		}
	})

	t.Run("a key the app declares and a binding reads in another tier is refused, though this tier does not need it", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), variables.Scope{Apps: []variables.App{{Name: "api"}}, Tier: environmentv1.Tier_TIER_PREVIEW, OtherTiers: []variables.BindingVariables{ordersBinding}})
		if err := g.RefuseIncomplete(); err != nil {
			t.Fatalf("RefuseIncomplete() = %v, want nothing missing for a binding this tier does not take", err)
		}
		_, err := g.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{{
			Key: "ORDERS_PASSWORD", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: false, Source: "resources/env.ts",
		}}})
		if err == nil {
			t.Fatal("DeclareEnv = nil, want the collision refused in every tier")
		}
		for _, want := range []string{"ORDERS_PASSWORD", "resources/env.ts", "bindings.postgres.orders"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("DeclareEnv = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("the variables editor shows a binding's variables as its group", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), variables.Scope{Bindings: []variables.BindingVariables{ordersBinding}})
		matrix := g.Matrix(nil)

		grouped := map[string]string{}
		for _, row := range matrix.Rows {
			grouped[row.Key] = row.Group
		}
		for _, key := range ordersBinding.Keys {
			if grouped[key] != "postgres.orders" {
				t.Errorf("row %s grouped under %q, want postgres.orders", key, grouped[key])
			}
		}
		found := false
		for _, group := range matrix.Groups {
			found = found || (group.Key == "postgres.orders" && group.Required)
		}
		if !found {
			t.Errorf("groups = %+v, want postgres.orders, required", matrix.Groups)
		}
	})

	t.Run("a variable two bindings read names both readers and leaves no group empty", func(t *testing.T) {
		t.Parallel()
		first := variables.BindingVariables{Group: "bucket.first", Site: "bindings.bucket.first", Keys: []string{"FIRST_BUCKET", "R2_ACCESS_KEY_ID"}}
		second := variables.BindingVariables{Group: "bucket.second", Site: "bindings.bucket.second", Keys: []string{"R2_ACCESS_KEY_ID"}}
		bound := []variables.BindingVariables{first, second}

		definitions, groups := variables.ImpliedDeclarations(variables.Scope{Bindings: bound})
		for _, group := range groups {
			members := 0
			for _, definition := range definitions {
				if definition.GetGroup() == group.GetKey() {
					members++
				}
			}
			if members == 0 {
				t.Errorf("group %s is declared with no variable in it", group.GetKey())
			}
		}
		for _, definition := range definitions {
			if definition.GetKey() == "R2_ACCESS_KEY_ID" && !strings.Contains(definition.GetDescription(), second.Site) {
				t.Errorf("R2_ACCESS_KEY_ID is described as %q, want it to name %s, which reads it too", definition.GetDescription(), second.Site)
			}
		}

		err := variables.RefuseImpliedInFolder(variables.Scope{Bindings: bound}, "R2_ACCESS_KEY_ID", "/web")
		for _, site := range []string{first.Site, second.Site} {
			if err == nil || !strings.Contains(err.Error(), site) {
				t.Errorf("RefuseImpliedInFolder = %v, want it to name %s", err, site)
			}
		}
	})
}
